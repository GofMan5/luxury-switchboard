import type { RecentError, RequestRule, RetryRule, RuleStats, RuleTestInput, RuleTestResult, RulesCatalog } from '../domain/rules'

/** The control plane's rules slice, as the workspace uses it. The operator
 * never sends an actor: the control plane treats a call without one as theirs. */
export interface RulesPort {
  list(signal?: AbortSignal): Promise<RulesCatalog>
  saveRequestRule(rule: RequestRule, signal?: AbortSignal): Promise<RequestRule>
  deleteRequestRule(id: string, signal?: AbortSignal): Promise<void>
  reorderRequestRules(ids: readonly string[], signal?: AbortSignal): Promise<readonly RequestRule[]>
  saveRetryRule(rule: RetryRule, signal?: AbortSignal): Promise<RetryRule>
  deleteRetryRule(id: string, signal?: AbortSignal): Promise<void>
  reorderRetryRules(ids: readonly string[], signal?: AbortSignal): Promise<readonly RetryRule[]>
  stats(signal?: AbortSignal): Promise<Readonly<Record<string, RuleStats>>>
  test(input: RuleTestInput, signal?: AbortSignal): Promise<RuleTestResult>
  recentErrors(limit: number, signal?: AbortSignal): Promise<readonly RecentError[]>
  /** `rules.changed`: someone (the operator elsewhere, or a plugin) changed a rule. */
  subscribe(listener: () => void): () => void
}
