/**
 * Relay rules as the control plane stores them, and the checks a rule must
 * pass before it is sent. The limits mirror the rules slice in Go exactly:
 * the editor refuses what the control plane would refuse, with the same
 * reasons, so a save failure is a surprise rather than the normal path.
 */

export const ENDPOINTS = ['responses', 'chat', 'messages', 'completions', 'embeddings', 'images', 'models', 'other'] as const
export type Endpoint = (typeof ENDPOINTS)[number]

export const ENDPOINT_LABELS: Readonly<Record<Endpoint, string>> = {
  responses: 'Responses',
  chat: 'Chat completions',
  messages: 'Messages',
  completions: 'Completions',
  embeddings: 'Embeddings',
  images: 'Images',
  models: 'Model list',
  other: 'Other',
}

export const BLOCK_STATUSES = [400, 403, 429, 503] as const
export type BlockStatus = (typeof BLOCK_STATUSES)[number]

export const BUDGET_PERIODS = ['hour', 'day'] as const
export type BudgetPeriod = (typeof BUDGET_PERIODS)[number]

export const RETRY_ACTIONS = ['retry', 'rotate_key', 'switch_provider', 'fail'] as const
export type RetryAction = (typeof RETRY_ACTIONS)[number]

export const RETRY_ACTION_LABELS: Readonly<Record<RetryAction, string>> = {
  retry: 'Retry',
  rotate_key: 'Rotate to the next key',
  switch_provider: 'Switch provider',
  fail: 'Fail at once',
}

export interface RuleEffects {
  readonly provider?: string
  readonly model?: string
  readonly block?: { readonly status: BlockStatus; readonly message: string }
  readonly rateLimit?: { readonly requests: number; readonly windowSeconds: number }
  readonly budget?: { readonly requests?: number; readonly tokens?: number; readonly period: BudgetPeriod }
  readonly headers?: Readonly<Record<string, string>>
  readonly tags?: readonly string[]
  readonly stop?: boolean
}

export interface RequestRule {
  readonly id: string
  readonly name: string
  readonly enabled: boolean
  /** "" for the operator, `plugin:<id>` for a plugin; assigned by the control plane. */
  readonly owner?: string
  readonly match: { readonly models: readonly string[]; readonly endpoints: readonly Endpoint[] }
  readonly effects: RuleEffects
}

export interface RetryRule {
  readonly id: string
  readonly name: string
  readonly enabled: boolean
  readonly owner?: string
  readonly match: {
    readonly statuses: readonly number[]
    readonly contains: readonly string[]
    readonly errorCodes: readonly string[]
    readonly providers: readonly string[]
    readonly models: readonly string[]
  }
  readonly action: RetryAction
  readonly maxAttempts: number
  readonly delayMs: number
}

export interface RuleStats {
  readonly hits: number
  readonly lastHitAt?: string
  readonly windowRequests: number
  readonly windowTokens: number
  readonly windowResetsAt?: string
}

export interface RecentError {
  readonly signature: string
  readonly status: number
  readonly errorCode: string
  /** Already redacted by the control plane, at most 200 characters. */
  readonly detail: string
  readonly providerName: string
  readonly model: string
  readonly count: number
  readonly lastAt: string
  /** The custom retry rule that now matches this error, if any. */
  readonly coveredBy?: string
}

export interface RuleTestInput {
  readonly model: string
  readonly path: string
  readonly status?: number
  readonly errorText?: string
  readonly errorCode?: string
  readonly providerId?: string
}

export interface RuleTestResult {
  /** Matching request-rule ids, in evaluation order. */
  readonly requestRules: readonly string[]
  /** The effects the relay would apply, merged like the relay merges them. */
  readonly effects: RuleEffects
  /** The retry rule that would handle the error; absent means built-in handling. */
  readonly retryRule?: string
}

export interface RulesCatalog {
  readonly requestRules: readonly RequestRule[]
  readonly retryRules: readonly RetryRule[]
}

export const RULE_LIMITS = {
  /** Per kind. */
  rules: 64,
  name: 60,
  globs: 16,
  glob: 200,
  provider: 64,
  model: 200,
  blockMessage: 200,
  rateRequests: 100_000,
  rateWindowSeconds: 86_400,
  budgetRequests: 10_000_000,
  budgetTokens: 10_000_000_000,
  headers: 8,
  headerValue: 256,
  tags: 8,
  statuses: 16,
  contains: 8,
  containsMin: 2,
  containsMax: 120,
  errorCodes: 8,
  providers: 16,
  maxAttempts: 10,
  delayMs: 60_000,
  testPath: 2_048,
  testErrorText: 2_048,
} as const

export const RULE_ID_PATTERN = /^[a-z0-9][a-z0-9_-]{0,47}$/u
export const HEADER_NAME_PATTERN = /^X-[A-Za-z0-9-]{1,62}$/u
export const TAG_PATTERN = /^[a-z0-9][a-z0-9_.-]{0,31}$/u
export const ERROR_CODE_PATTERN = /^[a-z][a-z0-9_]{0,63}$/u
// eslint-disable-next-line no-control-regex -- the control characters are what this finds
const CONTROL = /[\u0000-\u001f\u007f]/u
const PRINTABLE_ASCII = /^[\x20-\x7e]+$/u
// Mirrors the backend's ForbiddenHeader: relay identity, proxy chain, preset
// client identity and anything credential-shaped.
const RESERVED_HEADER = /^(x-switchboard-|x-forwarded-|x-stainless-)|^x-real-ip$|auth|token|key|secret|session|cookie|password|signature/iu

/** A field the editor can point at, and why it was refused. */
export interface RuleProblem {
  readonly field: string
  readonly message: string
}

/** Length in code points, the unit every rule limit counts. */
export const codePoints = (value: string): number => [...value].length

function textProblem(value: string, min: number, max: number): boolean {
  const text = value.trim()
  const length = codePoints(text)
  return length < min || length > max || CONTROL.test(text)
}

const whole = (value: unknown, min: number, max: number): boolean =>
  typeof value === 'number' && Number.isInteger(value) && value >= min && value <= max

function checkIdentity(rule: { id: string; name: string }, problems: RuleProblem[]): void {
  if (!RULE_ID_PATTERN.test(rule.id)) problems.push({ field: 'id', message: 'Use 1–48 lowercase letters, digits, - or _, starting with a letter or digit.' })
  if (textProblem(rule.name, 1, RULE_LIMITS.name)) problems.push({ field: 'name', message: `Name the rule in 1–${RULE_LIMITS.name} characters.` })
}

function checkGlobs(globs: readonly string[], field: string, problems: RuleProblem[]): void {
  if (globs.length > RULE_LIMITS.globs) problems.push({ field, message: `List at most ${RULE_LIMITS.globs} model patterns.` })
  else if (globs.some((glob) => textProblem(glob, 1, RULE_LIMITS.glob))) problems.push({ field, message: `Each model pattern is 1–${RULE_LIMITS.glob} characters.` })
}

function checkEffects(effects: RuleEffects, problems: RuleProblem[]): void {
  if (effects.provider !== undefined && textProblem(effects.provider, 1, RULE_LIMITS.provider)) problems.push({ field: 'provider', message: 'Pick a provider.' })
  if (effects.model !== undefined && textProblem(effects.model, 1, RULE_LIMITS.model)) problems.push({ field: 'model', message: `The upstream model is 1–${RULE_LIMITS.model} characters.` })
  if (effects.block) {
    if (!BLOCK_STATUSES.includes(effects.block.status)) problems.push({ field: 'blockStatus', message: 'Answer with 400, 403, 429 or 503.' })
    if (textProblem(effects.block.message, 1, RULE_LIMITS.blockMessage)) problems.push({ field: 'blockMessage', message: `The message is 1–${RULE_LIMITS.blockMessage} characters of plain text.` })
  }
  if (effects.rateLimit) {
    if (!whole(effects.rateLimit.requests, 1, RULE_LIMITS.rateRequests)) problems.push({ field: 'rateRequests', message: `Allow 1–${RULE_LIMITS.rateRequests.toLocaleString('en-US')} requests.` })
    if (!whole(effects.rateLimit.windowSeconds, 1, RULE_LIMITS.rateWindowSeconds)) problems.push({ field: 'rateWindow', message: `The window is 1–${RULE_LIMITS.rateWindowSeconds.toLocaleString('en-US')} seconds.` })
  }
  if (effects.budget) {
    const { requests, tokens, period } = effects.budget
    if (requests === undefined && tokens === undefined) problems.push({ field: 'budget', message: 'Set a request budget, a token budget or both.' })
    if (requests !== undefined && !whole(requests, 1, RULE_LIMITS.budgetRequests)) problems.push({ field: 'budgetRequests', message: `A request budget is 1–${RULE_LIMITS.budgetRequests.toLocaleString('en-US')}.` })
    if (tokens !== undefined && !whole(tokens, 1, RULE_LIMITS.budgetTokens)) problems.push({ field: 'budgetTokens', message: `A token budget is 1–${RULE_LIMITS.budgetTokens.toLocaleString('en-US')}.` })
    if (!BUDGET_PERIODS.includes(period)) problems.push({ field: 'budgetPeriod', message: 'Budgets reset every hour or every day.' })
  }
  if (effects.headers) {
    const names = Object.keys(effects.headers)
    const seen = new Set<string>()
    if (names.length > RULE_LIMITS.headers) problems.push({ field: 'headers', message: `Add at most ${RULE_LIMITS.headers} headers.` })
    for (const name of names) {
      const folded = name.toLowerCase()
      if (!HEADER_NAME_PATTERN.test(name)) problems.push({ field: 'headers', message: `"${name.slice(0, 64)}" is not a header name: use X- followed by letters, digits or -.` })
      else if (RESERVED_HEADER.test(name)) problems.push({ field: 'headers', message: `${name} is reserved: X-Switchboard-*, X-Forwarded-*, X-Real-IP, X-Stainless-* and credential-like names cannot be set.` })
      else if (seen.has(folded)) problems.push({ field: 'headers', message: `${name} is listed twice.` })
      seen.add(folded)
      const value = effects.headers[name] ?? ''
      if (value.length > RULE_LIMITS.headerValue || !PRINTABLE_ASCII.test(value)) problems.push({ field: 'headers', message: `The value of ${HEADER_NAME_PATTERN.test(name) ? name : 'a header'} is 1–${RULE_LIMITS.headerValue} printable ASCII characters.` })
    }
  }
  if (effects.tags) {
    if (effects.tags.length > RULE_LIMITS.tags) problems.push({ field: 'tags', message: `Add at most ${RULE_LIMITS.tags} tags.` })
    else if (effects.tags.some((tag) => !TAG_PATTERN.test(tag))) problems.push({ field: 'tags', message: 'Tags are 1–32 lowercase letters, digits, ., - or _, starting with a letter or digit.' })
  }
}

/** Everything the control plane would refuse in a request rule. */
export function requestRuleProblems(rule: RequestRule): RuleProblem[] {
  const problems: RuleProblem[] = []
  checkIdentity(rule, problems)
  checkGlobs(rule.match.models, 'models', problems)
  if (new Set(rule.match.endpoints).size !== rule.match.endpoints.length || rule.match.endpoints.some((endpoint) => !ENDPOINTS.includes(endpoint))) {
    problems.push({ field: 'endpoints', message: 'Pick each endpoint at most once.' })
  }
  checkEffects(rule.effects, problems)
  return problems
}

/** Everything the control plane would refuse in a retry rule. */
export function retryRuleProblems(rule: RetryRule): RuleProblem[] {
  const problems: RuleProblem[] = []
  checkIdentity(rule, problems)
  const { statuses, contains, errorCodes, providers, models } = rule.match
  if (statuses.length > RULE_LIMITS.statuses) problems.push({ field: 'statuses', message: `List at most ${RULE_LIMITS.statuses} statuses.` })
  else if (statuses.some((status) => !whole(status, 400, 599))) problems.push({ field: 'statuses', message: 'Statuses are error codes from 400 to 599.' })
  if (contains.length > RULE_LIMITS.contains) problems.push({ field: 'contains', message: `List at most ${RULE_LIMITS.contains} phrases.` })
  else if (contains.some((phrase) => textProblem(phrase, RULE_LIMITS.containsMin, RULE_LIMITS.containsMax))) problems.push({ field: 'contains', message: `Each phrase is ${RULE_LIMITS.containsMin}–${RULE_LIMITS.containsMax} characters.` })
  if (errorCodes.length > RULE_LIMITS.errorCodes) problems.push({ field: 'errorCodes', message: `List at most ${RULE_LIMITS.errorCodes} error codes.` })
  else if (errorCodes.some((code) => !ERROR_CODE_PATTERN.test(code))) problems.push({ field: 'errorCodes', message: 'Error codes are lowercase words joined with _, such as upstream_status.' })
  if (providers.length > RULE_LIMITS.providers) problems.push({ field: 'providers', message: `Pick at most ${RULE_LIMITS.providers} providers.` })
  else if (providers.some((provider) => textProblem(provider, 1, RULE_LIMITS.provider))) problems.push({ field: 'providers', message: 'A provider id is 1–64 characters.' })
  checkGlobs(models, 'retryModels', problems)
  if (statuses.length === 0 && contains.length === 0 && errorCodes.length === 0) {
    problems.push({ field: 'match', message: 'Match on at least one status, phrase or error code.' })
  }
  if (!RETRY_ACTIONS.includes(rule.action)) problems.push({ field: 'action', message: 'Pick what the relay does.' })
  if (!whole(rule.maxAttempts, 1, RULE_LIMITS.maxAttempts)) problems.push({ field: 'maxAttempts', message: `Attempts are 1–${RULE_LIMITS.maxAttempts}.` })
  if (!whole(rule.delayMs, 0, RULE_LIMITS.delayMs)) problems.push({ field: 'delayMs', message: `The delay is 0–${RULE_LIMITS.delayMs.toLocaleString('en-US')} ms.` })
  return problems
}

// Pasted error bodies keep their line breaks and tabs; nothing else invisible.
// eslint-disable-next-line no-control-regex -- the control characters are what this finds
const TEST_TEXT_CONTROL = /[\u0000-\u0008\u000b-\u001f\u007f]/u

/** Everything `rules.test` would refuse. Empty optional fields are absent. */
export function ruleTestProblems(input: RuleTestInput): RuleProblem[] {
  const problems: RuleProblem[] = []
  const path = input.path.trim()
  if (path === '' || codePoints(path) > RULE_LIMITS.testPath || CONTROL.test(path)) problems.push({ field: 'path', message: `Enter a request path of 1–${RULE_LIMITS.testPath.toLocaleString('en-US')} characters.` })
  if (codePoints(input.model) > RULE_LIMITS.model || CONTROL.test(input.model)) problems.push({ field: 'model', message: `A model is at most ${RULE_LIMITS.model} characters.` })
  if (input.status !== undefined && !whole(input.status, 400, 599)) problems.push({ field: 'status', message: 'A status is an error code from 400 to 599.' })
  if (input.errorCode && !ERROR_CODE_PATTERN.test(input.errorCode)) problems.push({ field: 'errorCode', message: 'Error codes are lowercase words joined with _, such as upstream_status.' })
  if (input.errorText && (codePoints(input.errorText) > RULE_LIMITS.testErrorText || TEST_TEXT_CONTROL.test(input.errorText))) problems.push({ field: 'errorText', message: `Error text is at most ${RULE_LIMITS.testErrorText.toLocaleString('en-US')} characters.` })
  if (input.providerId && textProblem(input.providerId, 1, RULE_LIMITS.provider)) problems.push({ field: 'providerId', message: 'A provider id is 1–64 characters.' })
  return problems
}

/** The plugin id behind an owner, or "" for the operator's own rules. */
export function ownerPlugin(owner: string | undefined): string {
  return owner?.startsWith('plugin:') ? owner.slice('plugin:'.length) : ''
}

/** Moves one id a step up or down; the list is unchanged at either end. */
export function moveId(ids: readonly string[], id: string, direction: -1 | 1): readonly string[] {
  const index = ids.indexOf(id)
  const target = index + direction
  if (index < 0 || target < 0 || target >= ids.length) return ids
  const next = [...ids]
  next[index] = next[target] as string
  next[target] = id
  return next
}

/** A fresh id from a rule name that no rule of either kind uses yet. */
export function ruleIdFromName(name: string, taken: ReadonlySet<string>): string {
  // Decomposed accents are dropped, not turned into separators: Ünïcode → unicode.
  const base = name.normalize('NFKD').replace(/\p{M}+/gu, '').toLowerCase().replace(/[^a-z0-9]+/gu, '-').replace(/^-+|-+$/gu, '').slice(0, 40).replace(/-+$/u, '') || 'rule'
  if (!taken.has(base)) return base
  for (let suffix = 2; suffix < 1_000; suffix++) {
    const candidate = `${base}-${suffix}`
    if (!taken.has(candidate)) return candidate
  }
  return `${base}-${Date.now().toString(36)}`.slice(0, 48)
}
