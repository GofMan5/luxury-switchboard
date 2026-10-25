import { formatClock, formatInteger } from '../../../shared/format/metrics'
import {
  ENDPOINT_LABELS, ENDPOINTS, ownerPlugin, RETRY_ACTION_LABELS,
  type RecentError, type RequestRule, type RetryRule, type RuleEffects, type RuleStats,
} from '../domain/rules'

/** Provider ids to the names the operator gave them. */
export type ProviderNames = ReadonlyMap<string, string>

const providerName = (id: string, names: ProviderNames): string => names.get(id) ?? id

function period(seconds: number): string {
  if (seconds % 86_400 === 0) return seconds === 86_400 ? 'day' : `${seconds / 86_400} days`
  if (seconds % 3_600 === 0) return seconds === 3_600 ? 'hour' : `${seconds / 3_600} h`
  if (seconds % 60 === 0) return seconds === 60 ? 'minute' : `${seconds / 60} min`
  return `${formatInteger(seconds)} s`
}

/** What a request rule applies to: `gpt-4* · Chat completions`. */
export function describeRequestMatch(rule: RequestRule): string {
  const models = rule.match.models.length ? rule.match.models.join(', ') : 'Any model'
  const endpoints = rule.match.endpoints.length === 0 || rule.match.endpoints.length === ENDPOINTS.length
    ? 'any endpoint'
    : rule.match.endpoints.map((endpoint) => ENDPOINT_LABELS[endpoint]).join(', ')
  return `${models} · ${endpoints}`
}

/** What a rule does, one short phrase per effect, in the relay's order. */
export function describeEffects(effects: RuleEffects, names: ProviderNames): string[] {
  const parts: string[] = []
  if (effects.block) parts.push(`Block with ${effects.block.status}`)
  if (effects.provider) parts.push(`Send to ${providerName(effects.provider, names)}`)
  if (effects.model) parts.push(`Use model ${effects.model}`)
  if (effects.rateLimit) parts.push(`At most ${formatInteger(effects.rateLimit.requests)} per ${period(effects.rateLimit.windowSeconds)}`)
  if (effects.budget) {
    const limits = [
      effects.budget.requests !== undefined ? `${formatInteger(effects.budget.requests)} requests` : '',
      effects.budget.tokens !== undefined ? `${formatInteger(effects.budget.tokens)} tokens` : '',
    ].filter(Boolean).join(' and ')
    parts.push(`Budget ${limits} per ${effects.budget.period}`)
  }
  const headers = Object.keys(effects.headers ?? {}).length
  if (headers) parts.push(headers === 1 ? 'Adds 1 header' : `Adds ${headers} headers`)
  if (effects.tags?.length) parts.push(`Tags ${effects.tags.join(', ')}`)
  if (effects.stop) parts.push('Stops here')
  return parts
}

/** What a retry rule catches: `502, 503 · "overloaded" · upstream_status`. */
export function describeRetryMatch(rule: RetryRule, names: ProviderNames): string {
  const { statuses, contains, errorCodes, providers, models } = rule.match
  const parts = [
    statuses.join(', '),
    contains.map((phrase) => `“${phrase}”`).join(', '),
    errorCodes.join(', '),
  ].filter(Boolean)
  if (providers.length) parts.push(`from ${providers.map((id) => providerName(id, names)).join(', ')}`)
  if (models.length) parts.push(`for ${models.join(', ')}`)
  return parts.join(' · ')
}

/** What the relay then does: `Retry up to 3 times, 500 ms apart`. */
export function describeRetryAction(rule: RetryRule): string {
  if (rule.action === 'fail') return 'Fail at once'
  const attempts = rule.maxAttempts === 1 ? 'once' : `up to ${rule.maxAttempts} times`
  const delay = rule.delayMs > 0 ? `, ${formatInteger(rule.delayMs)} ms apart` : ''
  return `${RETRY_ACTION_LABELS[rule.action]} ${attempts}${delay}`
}

/** The status a failure came back with, or `No response` for a transport
 * error, next to the relay's error code. */
export function errorKind(error: RecentError): { title: string; code: string } {
  return { title: error.status > 0 ? String(error.status) : 'No response', code: error.errorCode }
}

/** "Plugin · <id>" for a plugin's rule; empty for the operator's own. */
export function ownerLabel(owner: string | undefined): string {
  const plugin = ownerPlugin(owner)
  return plugin ? `Plugin · ${plugin}` : ''
}

export interface CounterText {
  readonly hits: string
  /** The open window or budget, when the rule has one. */
  readonly window: string
  /** When that window starts over, or when the rule last matched. */
  readonly detail: string
}

const hitsText = (hits: number): string => (hits === 1 ? '1 hit' : `${formatInteger(hits)} hits`)

/** The counters `rules.stats` reports for a request rule. A budget's period
 * wins over the rate window, the same way the control plane counts them. */
export function requestRuleCounters(rule: RequestRule, stats: RuleStats | undefined): CounterText {
  const hits = hitsText(stats?.hits ?? 0)
  const resets = stats?.windowResetsAt ? `resets ${formatClock(stats.windowResetsAt)}` : ''
  const { budget, rateLimit } = rule.effects
  if (budget) {
    const used = [
      budget.requests !== undefined ? `${formatInteger(stats?.windowRequests ?? 0)} / ${formatInteger(budget.requests)} req` : '',
      budget.tokens !== undefined ? `${formatInteger(stats?.windowTokens ?? 0)} / ${formatInteger(budget.tokens)} tok` : '',
    ].filter(Boolean).join(' · ')
    return { hits, window: `${used} this ${budget.period}`, detail: resets }
  }
  if (rateLimit) {
    const open = stats?.windowResetsAt ? stats.windowRequests : 0
    return { hits, window: `${formatInteger(open)} / ${formatInteger(rateLimit.requests)} per ${period(rateLimit.windowSeconds)}`, detail: resets }
  }
  return { hits, window: '', detail: lastHit(stats) }
}

export function retryRuleCounters(stats: RuleStats | undefined): CounterText {
  return { hits: hitsText(stats?.hits ?? 0), window: '', detail: lastHit(stats) }
}

function lastHit(stats: RuleStats | undefined): string {
  const clock = stats?.lastHitAt ? formatClock(stats.lastHitAt) : '—'
  return clock === '—' ? '' : `last ${clock}`
}
