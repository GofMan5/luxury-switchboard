import type { ControlPlaneSession } from '../../../platform/stdio/session'
import type { RulesPort } from '../application/rules-port'
import type { RecentError, RequestRule, RetryRule, RuleStats, RuleTestInput, RuleTestResult, RulesCatalog } from '../domain/rules'

export class StdioRulesPort implements RulesPort {
  readonly #session: ControlPlaneSession

  constructor(session: ControlPlaneSession) {
    this.#session = session
  }

  async list(signal?: AbortSignal): Promise<RulesCatalog> {
    const result = await this.#session.call<RulesCatalog>('rules.list', {}, signal)
    return { requestRules: result.requestRules ?? [], retryRules: result.retryRules ?? [] }
  }

  saveRequestRule(rule: RequestRule, signal?: AbortSignal): Promise<RequestRule> {
    return this.#session.call<RequestRule>('rules.saveRequestRule', { rule }, signal)
  }

  async deleteRequestRule(id: string, signal?: AbortSignal): Promise<void> {
    await this.#session.call('rules.deleteRequestRule', { id }, signal)
  }

  async reorderRequestRules(ids: readonly string[], signal?: AbortSignal): Promise<readonly RequestRule[]> {
    const result = await this.#session.call<{ requestRules: readonly RequestRule[] }>('rules.reorderRequestRules', { ids }, signal)
    return result.requestRules ?? []
  }

  saveRetryRule(rule: RetryRule, signal?: AbortSignal): Promise<RetryRule> {
    return this.#session.call<RetryRule>('rules.saveRetryRule', { rule }, signal)
  }

  async deleteRetryRule(id: string, signal?: AbortSignal): Promise<void> {
    await this.#session.call('rules.deleteRetryRule', { id }, signal)
  }

  async reorderRetryRules(ids: readonly string[], signal?: AbortSignal): Promise<readonly RetryRule[]> {
    const result = await this.#session.call<{ retryRules: readonly RetryRule[] }>('rules.reorderRetryRules', { ids }, signal)
    return result.retryRules ?? []
  }

  async stats(signal?: AbortSignal): Promise<Readonly<Record<string, RuleStats>>> {
    const result = await this.#session.call<{ rules: Readonly<Record<string, RuleStats>> }>('rules.stats', {}, signal)
    return result.rules ?? {}
  }

  test(input: RuleTestInput, signal?: AbortSignal): Promise<RuleTestResult> {
    return this.#session.call<RuleTestResult>('rules.test', input, signal)
  }

  async recentErrors(limit: number, signal?: AbortSignal): Promise<readonly RecentError[]> {
    const result = await this.#session.call<{ errors: readonly RecentError[] }>('rules.recentErrors', { limit }, signal)
    return result.errors ?? []
  }

  subscribe(listener: () => void): () => void {
    return this.#session.subscribe('rules.changed', () => listener())
  }
}
