import type { RetryRuleDraftSource } from '../../../shared/contracts/rules-link'
import {
  BLOCK_STATUSES, ruleIdFromName,
  type BlockStatus, type BudgetPeriod, type Endpoint, type RequestRule, type RetryAction, type RetryRule, type RuleEffects,
  type RuleProblem, type RuleTestInput,
} from '../domain/rules'

/** Error codes the relay records most often, named in the editor's hint. */
export const COMMON_ERROR_CODES = ['upstream_status', 'transport', 'timeout', 'stream_incomplete'] as const

export const WINDOW_UNITS = [
  { id: 'second', label: 'seconds', seconds: 1 },
  { id: 'minute', label: 'minutes', seconds: 60 },
  { id: 'hour', label: 'hours', seconds: 3_600 },
  { id: 'day', label: 'days', seconds: 86_400 },
] as const
export type WindowUnit = (typeof WINDOW_UNITS)[number]['id']

export interface HeaderRow {
  /** Stable React key; never sent. */
  readonly key: number
  readonly name: string
  readonly value: string
}

/** The request-rule editor's fields, as text the operator types. */
export interface RequestRuleForm {
  readonly id: string
  readonly name: string
  readonly enabled: boolean
  readonly owner: string
  readonly models: string
  readonly endpoints: readonly Endpoint[]
  readonly provider: string
  readonly model: string
  readonly blockOn: boolean
  readonly blockStatus: BlockStatus
  readonly blockMessage: string
  readonly rateOn: boolean
  readonly rateRequests: string
  readonly rateWindow: string
  readonly rateUnit: WindowUnit
  readonly budgetOn: boolean
  readonly budgetRequests: string
  readonly budgetTokens: string
  readonly budgetPeriod: BudgetPeriod
  readonly headers: readonly HeaderRow[]
  readonly tags: string
  readonly stop: boolean
}

export interface RetryRuleForm {
  readonly id: string
  readonly name: string
  readonly enabled: boolean
  readonly owner: string
  readonly statuses: string
  /** One phrase per line: a phrase may itself contain a comma. */
  readonly contains: string
  readonly errorCodes: string
  readonly providers: readonly string[]
  readonly models: string
  readonly action: RetryAction
  readonly maxAttempts: string
  readonly delayMs: string
}

/** Comma- or newline-separated entries, trimmed, blanks and repeats dropped. */
export function splitList(text: string, separators: RegExp = /[,\n]/u): string[] {
  const seen = new Set<string>()
  for (const entry of text.split(separators)) {
    const value = entry.trim()
    if (value) seen.add(value)
  }
  return [...seen]
}

/** A typed number: blank is absent; anything else is checked by the domain. */
function typedNumber(text: string): number | undefined {
  const value = text.trim()
  return value === '' ? undefined : Number(value)
}

const required = (text: string): number => typedNumber(text) ?? Number.NaN

function windowParts(seconds: number): { value: string; unit: WindowUnit } {
  const unit = [...WINDOW_UNITS].reverse().find((entry) => seconds % entry.seconds === 0) ?? WINDOW_UNITS[0]
  return { value: String(seconds / unit.seconds), unit: unit.id }
}

let headerKey = 0
export const headerRow = (name = '', value = ''): HeaderRow => ({ key: ++headerKey, name, value })

export function requestRuleForm(rule: RequestRule | null): RequestRuleForm {
  const effects = rule?.effects ?? {}
  const window = windowParts(effects.rateLimit?.windowSeconds ?? 60)
  return {
    id: rule?.id ?? '',
    name: rule?.name ?? '',
    enabled: rule?.enabled ?? true,
    owner: rule?.owner ?? '',
    models: rule?.match.models.join(', ') ?? '',
    endpoints: rule?.match.endpoints ?? [],
    provider: effects.provider ?? '',
    model: effects.model ?? '',
    blockOn: Boolean(effects.block),
    blockStatus: effects.block?.status ?? 403,
    blockMessage: effects.block?.message ?? '',
    rateOn: Boolean(effects.rateLimit),
    rateRequests: effects.rateLimit ? String(effects.rateLimit.requests) : '',
    rateWindow: window.value,
    rateUnit: window.unit,
    budgetOn: Boolean(effects.budget),
    budgetRequests: effects.budget?.requests !== undefined ? String(effects.budget.requests) : '',
    budgetTokens: effects.budget?.tokens !== undefined ? String(effects.budget.tokens) : '',
    budgetPeriod: effects.budget?.period ?? 'day',
    headers: Object.entries(effects.headers ?? {}).map(([name, value]) => headerRow(name, value)),
    tags: effects.tags?.join(', ') ?? '',
    stop: effects.stop ?? false,
  }
}

/** The rule a form describes. Absent effects are left out, never sent as null. */
export function requestRuleFromForm(form: RequestRuleForm): RequestRule {
  const unitSeconds = WINDOW_UNITS.find((unit) => unit.id === form.rateUnit)?.seconds ?? 1
  const headers = form.headers.filter((row) => row.name.trim() || row.value.trim())
  const tags = splitList(form.tags, /[,\s]+/u)
  const effects: RuleEffects = {
    ...(form.provider ? { provider: form.provider } : {}),
    ...(form.model.trim() ? { model: form.model.trim() } : {}),
    ...(form.blockOn ? { block: { status: form.blockStatus, message: form.blockMessage.trim() } } : {}),
    ...(form.rateOn ? { rateLimit: { requests: required(form.rateRequests), windowSeconds: required(form.rateWindow) * unitSeconds } } : {}),
    ...(form.budgetOn ? {
      budget: {
        ...(typedNumber(form.budgetRequests) !== undefined ? { requests: typedNumber(form.budgetRequests) } : {}),
        ...(typedNumber(form.budgetTokens) !== undefined ? { tokens: typedNumber(form.budgetTokens) } : {}),
        period: form.budgetPeriod,
      },
    } : {}),
    ...(headers.length ? { headers: Object.fromEntries(headers.map((row) => [row.name.trim(), row.value])) } : {}),
    ...(tags.length ? { tags } : {}),
    ...(form.stop ? { stop: true } : {}),
  }
  return {
    id: form.id.trim(),
    name: form.name.trim(),
    enabled: form.enabled,
    owner: form.owner,
    match: { models: splitList(form.models), endpoints: form.endpoints },
    effects,
  }
}

/** Headers listed twice collapse to one object key, so the editor checks rows itself. */
export function duplicateHeader(form: RequestRuleForm): string {
  const seen = new Set<string>()
  for (const row of form.headers) {
    const name = row.name.trim().toLowerCase()
    if (!name) continue
    if (seen.has(name)) return row.name.trim()
    seen.add(name)
  }
  return ''
}

const DEFAULT_RETRY = { action: 'retry' as RetryAction, maxAttempts: '3', delayMs: '1000' }

export function retryRuleForm(rule: RetryRule | null, draft?: RetryRuleDraftSource | null): RetryRuleForm {
  if (rule) {
    return {
      id: rule.id, name: rule.name, enabled: rule.enabled, owner: rule.owner ?? '',
      statuses: rule.match.statuses.join(', '),
      contains: rule.match.contains.join('\n'),
      errorCodes: rule.match.errorCodes.join(', '),
      providers: rule.match.providers,
      models: rule.match.models.join(', '),
      action: rule.action,
      maxAttempts: String(rule.maxAttempts),
      delayMs: String(rule.delayMs),
    }
  }
  const status = draft?.status
  const code = draft?.errorCode
  return {
    id: '', enabled: true, owner: '',
    name: draft ? `Handle ${[status, code].filter((part) => part !== undefined).join(' ') || 'this error'}` : '',
    statuses: status !== undefined ? String(status) : '',
    contains: draft?.detail ?? '',
    errorCodes: code ?? '',
    providers: [],
    models: '',
    // A 429 is the provider asking for patience: wait longer between tries.
    ...DEFAULT_RETRY,
    ...(status === 429 ? { delayMs: '5000' } : {}),
  }
}

export function retryRuleFromForm(form: RetryRuleForm): RetryRule {
  const fail = form.action === 'fail'
  return {
    id: form.id.trim(),
    name: form.name.trim(),
    enabled: form.enabled,
    owner: form.owner,
    match: {
      statuses: splitList(form.statuses, /[,\s]+/u).map(Number),
      contains: splitList(form.contains, /\n/u),
      errorCodes: splitList(form.errorCodes, /[,\s]+/u),
      providers: form.providers,
      models: splitList(form.models),
    },
    action: form.action,
    // Failing at once has no attempts to count; the control plane still wants the fields.
    maxAttempts: fail ? 1 : required(form.maxAttempts),
    delayMs: fail ? 0 : required(form.delayMs),
  }
}

/** Problems the domain cannot see: two header rows with one name, an id another rule holds. */
export function editorProblems(rule: readonly RuleProblem[], extra: { duplicateHeader?: string; idTaken?: boolean }): RuleProblem[] {
  const problems = [...rule]
  if (extra.idTaken) problems.unshift({ field: 'id', message: 'Another rule already uses this id.' })
  const twice = extra.duplicateHeader ? `${extra.duplicateHeader} is listed twice.` : ''
  if (twice && !problems.some((problem) => problem.message.toLowerCase() === twice.toLowerCase())) problems.push({ field: 'headers', message: twice })
  return problems
}

/** The first reason one of these fields was refused, once the operator has tried to save. */
export function problemFinder(problems: readonly RuleProblem[], shown: boolean) {
  return (...fields: string[]): string | undefined =>
    shown ? problems.find((problem) => fields.includes(problem.field))?.message : undefined
}

/** Moves focus to the first refused control, so a keyboard user lands on the fix. */
export function focusFirstInvalid(form: HTMLElement | null): void {
  queueMicrotask(() => form?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus())
}

/** The tester's fields, as typed. */
export interface TesterForm {
  readonly model: string
  readonly path: string
  readonly status: string
  readonly errorCode: string
  readonly errorText: string
  readonly providerId: string
}

/** The request the tester describes; blank optional fields stay absent, and
 * only the retry tester sends an error. A status that is not a number stays
 * NaN so the domain check names it. */
export function testInput(form: TesterForm, withError: boolean): RuleTestInput {
  const status = form.status.trim()
  return {
    model: form.model.trim(),
    path: form.path.trim(),
    ...(withError && status ? { status: /^\d+$/u.test(status) ? Number(status) : Number.NaN } : {}),
    ...(withError && form.errorCode.trim() ? { errorCode: form.errorCode.trim() } : {}),
    ...(withError && form.errorText.trim() ? { errorText: form.errorText } : {}),
    ...(withError && form.providerId ? { providerId: form.providerId } : {}),
  }
}

/** The id a new rule gets from its name, unless the operator typed one. */
export function suggestedId(name: string, taken: ReadonlySet<string>): string {
  return name.trim() ? ruleIdFromName(name, taken) : ''
}

export const isBlockStatus = (value: number): value is BlockStatus => (BLOCK_STATUSES as readonly number[]).includes(value)
