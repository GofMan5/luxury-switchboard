/**
 * Development fixture for the `rules.*` commands: an in-memory rule store
 * with the control plane's validation, ownership, ordering, counters, tester
 * and recent-error grouping, so the Rules workspace can be exercised in a
 * plain browser. Matching here is a readable approximation of the relay's,
 * enough to make the tester and "Covered by" believable; it is not the
 * contract. Dev-only, like the session that owns it.
 */
import { ControlPlaneError } from '../shared/contracts/protocol'
import {
  ENDPOINTS, RULE_LIMITS, requestRuleProblems, retryRuleProblems, ruleTestProblems,
  type Endpoint, type RecentError, type RequestRule, type RetryRule, type RuleEffects, type RuleStats, type RuleTestInput,
} from '../features/rules/domain/rules'

const ago = (milliseconds: number): string => new Date(Date.now() - milliseconds).toISOString()

const SEED_REQUEST_RULES: RequestRule[] = [
  {
    id: 'cheap-drafts', name: 'Cheap drafts', enabled: true, owner: '',
    match: { models: ['gpt-4o-mini*', 'draft-*'], endpoints: ['chat', 'responses'] },
    effects: { provider: 'sigma-llm', model: 'glm-5.3', tags: ['drafts'] },
  },
  {
    id: 'image-budget', name: 'Image budget', enabled: true, owner: '',
    match: { models: [], endpoints: ['images'] },
    effects: { budget: { requests: 200, period: 'day' }, rateLimit: { requests: 10, windowSeconds: 60 } },
  },
  {
    id: 'team-header', name: 'Team header', enabled: true, owner: 'plugin:team-tags',
    match: { models: ['*'], endpoints: [] },
    effects: { headers: { 'X-Team': 'research' } },
  },
  {
    id: 'no-embeddings', name: 'No embeddings', enabled: false, owner: '',
    match: { models: [], endpoints: ['embeddings'] },
    effects: { block: { status: 403, message: 'Embeddings are not available on this relay.' }, stop: true },
  },
]

const SEED_RETRY_RULES: RetryRule[] = [
  {
    id: 'overloaded-switch', name: 'Overloaded: switch provider', enabled: true, owner: '',
    match: { statuses: [529, 503], contains: ['overloaded'], errorCodes: [], providers: [], models: [] },
    action: 'switch_provider', maxAttempts: 2, delayMs: 250,
  },
  {
    id: 'quota-rotate', name: 'Quota: rotate key', enabled: true, owner: '',
    match: { statuses: [429], contains: ['insufficient quota'], errorCodes: [], providers: ['north-relay'], models: [] },
    action: 'rotate_key', maxAttempts: 3, delayMs: 0,
  },
]

interface FixtureFailure {
  readonly status: number
  readonly errorCode: string
  readonly detail: string
  readonly providerId: string
  readonly providerName: string
  readonly model: string
  readonly count: number
  readonly ageMs: number
}

const FAILURES: readonly FixtureFailure[] = [
  { status: 529, errorCode: 'upstream_status', detail: 'The model is overloaded. Please try again later.', providerId: 'vendor-hub', providerName: 'Vendor Hub', model: 'qwen3.8-max-0902', count: 14, ageMs: 90_000 },
  { status: 429, errorCode: 'upstream_status', detail: 'You exceeded your current quota (insufficient_quota).', providerId: 'north-relay', providerName: 'North Relay', model: 'glm-5.3', count: 6, ageMs: 420_000 },
  { status: 400, errorCode: 'upstream_status', detail: 'context_length_exceeded: the request is 131,204 tokens, the model allows 128,000.', providerId: 'sigma-llm', providerName: 'Sigma LLM', model: 'glm-5.3', count: 3, ageMs: 1_260_000 },
  { status: 0, errorCode: 'transport', detail: 'connection reset by peer', providerId: 'vendor-hub', providerName: 'Vendor Hub', model: 'qwen3.8-max-0902', count: 2, ageMs: 2_700_000 },
  { status: 502, errorCode: 'stream_incomplete', detail: 'The upstream stream ended before a finish reason.', providerId: 'north-relay', providerName: 'North Relay', model: 'gpt-4o-mini', count: 1, ageMs: 5_400_000 },
]

function globMatch(pattern: string, value: string): boolean {
  const source = pattern.toLowerCase().replace(/[.+?^${}()|[\]\\]/gu, '\\$&').replace(/\*/gu, '.*')
  return new RegExp(`^${source}$`, 'u').test(value.toLowerCase())
}

function endpointOf(path: string): Endpoint {
  const route = path.toLowerCase().replace(/\?.*$/su, '')
  if (route.endsWith('/responses')) return 'responses'
  if (route.endsWith('/chat/completions')) return 'chat'
  if (route.endsWith('/messages')) return 'messages'
  if (route.endsWith('/completions')) return 'completions'
  if (route.endsWith('/embeddings')) return 'embeddings'
  if (route.includes('/images/')) return 'images'
  if (route.endsWith('/models')) return 'models'
  return 'other'
}

const normalize = (text: string): string => text.toLowerCase().replace(/[_-]/gu, ' ')

function requestMatches(rule: RequestRule, model: string, endpoint: Endpoint): boolean {
  const models = rule.match.models.length === 0 || rule.match.models.some((pattern) => globMatch(pattern, model))
  const endpoints = rule.match.endpoints.length === 0 || rule.match.endpoints.length === ENDPOINTS.length || rule.match.endpoints.includes(endpoint)
  return rule.enabled && models && endpoints
}

interface FailureFacts {
  readonly status: number
  readonly errorCode: string
  readonly text: string
  readonly providerId: string
  readonly model: string
}

function retryMatches(rule: RetryRule, failure: FailureFacts): boolean {
  const { statuses, contains, errorCodes, providers, models } = rule.match
  const text = normalize(failure.text)
  return rule.enabled
    && (statuses.length === 0 || statuses.includes(failure.status))
    && (contains.length === 0 || contains.some((phrase) => text.includes(normalize(phrase))))
    && (errorCodes.length === 0 || errorCodes.includes(failure.errorCode))
    && (providers.length === 0 || providers.includes(failure.providerId))
    && (models.length === 0 || models.some((pattern) => globMatch(pattern, failure.model)))
}

/** Merged like the relay: the first rule to set a provider, model, block,
 * rate limit or budget wins; headers and tags accumulate; `stop` ends it. */
function evaluate(rules: readonly RequestRule[], model: string, path: string): { ids: string[]; effects: RuleEffects } {
  const endpoint = endpointOf(path)
  const ids: string[] = []
  let effects: RuleEffects = {}
  for (const rule of rules) {
    if (!requestMatches(rule, model, endpoint)) continue
    ids.push(rule.id)
    const next = rule.effects
    effects = {
      ...(effects.provider ?? next.provider ? { provider: effects.provider ?? next.provider } : {}),
      ...(effects.model ?? next.model ? { model: effects.model ?? next.model } : {}),
      ...(effects.block ?? next.block ? { block: effects.block ?? next.block } : {}),
      ...(effects.rateLimit ?? next.rateLimit ? { rateLimit: effects.rateLimit ?? next.rateLimit } : {}),
      ...(effects.budget ?? next.budget ? { budget: effects.budget ?? next.budget } : {}),
      ...(effects.headers || next.headers ? { headers: { ...next.headers, ...effects.headers } } : {}),
      ...(effects.tags || next.tags ? { tags: [...new Set([...(effects.tags ?? []), ...(next.tags ?? [])])] } : {}),
      ...(next.stop ? { stop: true } : {}),
    }
    if (next.stop) break
  }
  return { ids, effects }
}

export class FixtureRules {
  readonly #emit: (topic: string, payload: unknown) => void
  #requestRules = SEED_REQUEST_RULES.map((rule) => ({ ...rule }))
  #retryRules = SEED_RETRY_RULES.map((rule) => ({ ...rule }))
  readonly #hits = new Map<string, { hits: number; lastHitAt?: string }>([
    ['cheap-drafts', { hits: 1_284, lastHitAt: ago(20_000) }],
    ['image-budget', { hits: 37, lastHitAt: ago(600_000) }],
    ['team-header', { hits: 5_906, lastHitAt: ago(4_000) }],
    ['overloaded-switch', { hits: 14, lastHitAt: ago(90_000) }],
  ])

  constructor(emit: (topic: string, payload: unknown) => void) {
    this.#emit = emit
  }

  answer(method: string, body: Record<string, unknown>): unknown {
    switch (method) {
      case 'rules.list': return { requestRules: this.#requestRules, retryRules: this.#retryRules }
      case 'rules.saveRequestRule': return this.#save('request', body.rule)
      case 'rules.saveRetryRule': return this.#save('retry', body.rule)
      case 'rules.deleteRequestRule': return this.#delete('request', body.id)
      case 'rules.deleteRetryRule': return this.#delete('retry', body.id)
      case 'rules.reorderRequestRules': return { requestRules: this.#reorder('request', body.ids) }
      case 'rules.reorderRetryRules': return { retryRules: this.#reorder('retry', body.ids) }
      case 'rules.stats': return { rules: this.#stats() }
      case 'rules.test': return this.#test(body)
      case 'rules.recentErrors': {
        const limit = body.limit ?? 20
        if (typeof limit !== 'number' || !Number.isInteger(limit) || limit < 1 || limit > 100) throw new ControlPlaneError('invalid_payload', 'Limit must be 1-100')
        return { errors: this.#recentErrors().slice(0, limit) }
      }
      default: throw new Error(`fixture session has no answer for ${method}`)
    }
  }

  #changed(): void {
    this.#emit('rules.changed', {})
  }

  #save(kind: 'request' | 'retry', value: unknown): RequestRule | RetryRule {
    if (typeof value !== 'object' || value === null) throw new ControlPlaneError('invalid_payload', 'rule is required')
    const rules: (RequestRule | RetryRule)[] = kind === 'request' ? this.#requestRules : this.#retryRules
    const candidate = value as RequestRule & RetryRule
    const problems = kind === 'request' ? requestRuleProblems(candidate) : retryRuleProblems(candidate)
    if (problems[0]) throw new ControlPlaneError('invalid_rule', problems[0].message)
    const other = kind === 'request' ? this.#retryRules : this.#requestRules
    if (other.some((rule) => rule.id === candidate.id)) throw new ControlPlaneError('invalid_rule', 'Another rule already uses this id.')
    const index = rules.findIndex((rule) => rule.id === candidate.id)
    if (index < 0 && rules.length >= RULE_LIMITS.rules) throw new ControlPlaneError('invalid_rule', `At most ${RULE_LIMITS.rules} rules of a kind.`)
    // The operator keeps an existing rule's owner; a new rule is the operator's.
    const saved = { ...candidate, owner: index < 0 ? '' : rules[index]?.owner ?? '' }
    if (index < 0) rules.push(saved)
    else rules[index] = saved
    this.#changed()
    return saved
  }

  #delete(kind: 'request' | 'retry', id: unknown): { deleted: true } {
    const rules: { id: string }[] = kind === 'request' ? this.#requestRules : this.#retryRules
    const index = rules.findIndex((rule) => rule.id === id)
    if (index < 0) throw new ControlPlaneError('rule_not_found', 'Rule not found')
    rules.splice(index, 1)
    this.#hits.delete(id as string)
    this.#changed()
    return { deleted: true }
  }

  #reorder(kind: 'request' | 'retry', ids: unknown): readonly (RequestRule | RetryRule)[] {
    const rules: (RequestRule | RetryRule)[] = kind === 'request' ? this.#requestRules : this.#retryRules
    const list = Array.isArray(ids) ? ids : []
    const same = list.length === rules.length && new Set(list).size === list.length && rules.every((rule) => list.includes(rule.id))
    if (!same) throw new ControlPlaneError('invalid_payload', 'ids must list every rule exactly once')
    const ordered = list.map((id) => rules.find((rule) => rule.id === id) as RequestRule & RetryRule)
    if (kind === 'request') this.#requestRules = ordered
    else this.#retryRules = ordered
    this.#changed()
    return ordered
  }

  #stats(): Record<string, RuleStats> {
    const stats: Record<string, RuleStats> = {}
    const hour = new Date()
    hour.setMinutes(60, 0, 0)
    const day = new Date()
    day.setHours(24, 0, 0, 0)
    for (const rule of this.#requestRules) {
      const seen = this.#hits.get(rule.id)
      const base = { hits: seen?.hits ?? 0, ...(seen?.lastHitAt ? { lastHitAt: seen.lastHitAt } : {}) }
      if (rule.effects.budget) {
        const resetsAt = (rule.effects.budget.period === 'hour' ? hour : day).toISOString()
        stats[rule.id] = { ...base, windowRequests: Math.min(seen?.hits ?? 0, 124), windowTokens: rule.effects.budget.tokens ? 48_200 : 0, windowResetsAt: resetsAt }
      } else if (rule.effects.rateLimit && seen?.lastHitAt) {
        const resetsAt = new Date(Date.parse(seen.lastHitAt) + rule.effects.rateLimit.windowSeconds * 1_000)
        stats[rule.id] = resetsAt.valueOf() > Date.now()
          ? { ...base, windowRequests: 3, windowTokens: 0, windowResetsAt: resetsAt.toISOString() }
          : { ...base, windowRequests: 0, windowTokens: 0 }
      } else {
        stats[rule.id] = { ...base, windowRequests: 0, windowTokens: 0 }
      }
    }
    for (const rule of this.#retryRules) {
      const seen = this.#hits.get(rule.id)
      stats[rule.id] = { hits: seen?.hits ?? 0, ...(seen?.lastHitAt ? { lastHitAt: seen.lastHitAt } : {}), windowRequests: 0, windowTokens: 0 }
    }
    return stats
  }

  #test(body: Record<string, unknown>): unknown {
    const text = (value: unknown) => (typeof value === 'string' && value !== '' ? value : undefined)
    const input: RuleTestInput = {
      model: typeof body.model === 'string' ? body.model : '',
      path: typeof body.path === 'string' ? body.path : '',
      ...(body.status !== undefined ? { status: body.status as number } : {}),
      ...(text(body.errorText) ? { errorText: text(body.errorText) } : {}),
      ...(text(body.errorCode) ? { errorCode: text(body.errorCode) } : {}),
      ...(text(body.providerId) ? { providerId: text(body.providerId) } : {}),
    }
    const problems = ruleTestProblems(input)
    if (problems[0]) throw new ControlPlaneError('invalid_payload', problems[0].message)
    const { ids, effects } = evaluate(this.#requestRules, input.model, input.path)
    const failed = input.status !== undefined || input.errorText || input.errorCode
    const retry = failed
      ? this.#retryRules.find((rule) => retryMatches(rule, {
          status: input.status ?? 0, errorCode: input.errorCode ?? '', text: input.errorText ?? '',
          providerId: input.providerId ?? '', model: effects.model ?? input.model,
        }))
      : undefined
    return { requestRules: ids, effects, ...(retry ? { retryRule: retry.id } : {}) }
  }

  #recentErrors(): RecentError[] {
    return FAILURES.map((failure, index) => {
      const covering = this.#retryRules.find((rule) => retryMatches(rule, { ...failure, text: failure.detail }))
      return {
        signature: `fixture-${index}-${failure.status}-${failure.errorCode}`,
        status: failure.status,
        errorCode: failure.errorCode,
        detail: failure.detail,
        providerName: failure.providerName,
        model: failure.model,
        count: failure.count,
        lastAt: ago(failure.ageMs),
        ...(covering ? { coveredBy: covering.id } : {}),
      }
    })
  }
}
