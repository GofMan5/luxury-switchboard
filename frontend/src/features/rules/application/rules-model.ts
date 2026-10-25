import { ControlPlaneError } from '../../../shared/contracts/protocol'
import { moveId, type RecentError, type RequestRule, type RetryRule, type RuleStats, type RuleTestInput, type RuleTestResult } from '../domain/rules'
import type { RulesPort } from './rules-port'

/** How many recent failures the Errors tab asks for. */
export const RECENT_ERROR_LIMIT = 50

export type LoadPhase = 'idle' | 'loading' | 'ready' | 'error'

export interface RulesState {
  readonly phase: LoadPhase
  /** Why the rules could not be loaded; shown instead of an empty list. */
  readonly loadError: string
  readonly requestRules: readonly RequestRule[]
  readonly retryRules: readonly RetryRule[]
  readonly stats: Readonly<Record<string, RuleStats>>
  readonly errorsPhase: LoadPhase
  readonly errorsMessage: string
  readonly recentErrors: readonly RecentError[]
  /** The rule id a change is in flight for; `*` for a reorder. */
  readonly pending: string
  /** The last list-level change that failed (toggle, move, delete). */
  readonly error: string
}

export type MutationResult = { readonly ok: true } | { readonly ok: false; readonly message: string }

const message = (error: unknown, fallback: string): string => (error instanceof ControlPlaneError ? error.message : fallback)

export class RulesModel {
  readonly #port: RulesPort
  #state: RulesState = {
    phase: 'idle', loadError: '', requestRules: [], retryRules: [], stats: {},
    errorsPhase: 'idle', errorsMessage: '', recentErrors: [], pending: '', error: '',
  }
  #listeners = new Set<() => void>()
  #unsubscribe: (() => void) | null = null
  #generation = 0
  #errorsGeneration = 0

  constructor(port: RulesPort) {
    this.#port = port
  }

  snapshot = (): RulesState => this.#state

  subscribe = (listener: () => void): (() => void) => {
    this.#listeners.add(listener)
    return () => this.#listeners.delete(listener)
  }

  /** Loads the rules and follows `rules.changed`. Safe to call again after a reconnect. */
  async connect(): Promise<void> {
    this.#unsubscribe ??= this.#port.subscribe(() => { void this.#refresh() })
    await this.load()
  }

  async load(): Promise<void> {
    const generation = ++this.#generation
    this.#set({ ...this.#state, phase: this.#state.phase === 'ready' ? 'ready' : 'loading', loadError: '' })
    try {
      const [catalog, stats] = await Promise.all([this.#port.list(), this.#port.stats().catch(() => this.#state.stats)])
      if (generation !== this.#generation) return
      this.#set({ ...this.#state, phase: 'ready', loadError: '', requestRules: catalog.requestRules, retryRules: catalog.retryRules, stats })
    } catch (error) {
      if (generation !== this.#generation) return
      this.#set({ ...this.#state, phase: 'error', loadError: message(error, 'Rules are unavailable.') })
    }
  }

  /** Hit and budget counters move with traffic, not with rule changes. */
  async refreshStats(): Promise<void> {
    if (this.#state.phase !== 'ready') return
    try {
      const stats = await this.#port.stats()
      this.#set({ ...this.#state, stats })
    } catch {
      // The counters stay as they were; the next refresh tries again.
    }
  }

  async loadErrors(): Promise<void> {
    const generation = ++this.#errorsGeneration
    this.#set({ ...this.#state, errorsPhase: 'loading', errorsMessage: '' })
    try {
      const recentErrors = await this.#port.recentErrors(RECENT_ERROR_LIMIT)
      if (generation !== this.#errorsGeneration) return
      this.#set({ ...this.#state, errorsPhase: 'ready', recentErrors })
    } catch (error) {
      if (generation !== this.#errorsGeneration) return
      // Unavailable history is not an empty history: say so instead of "no errors".
      this.#set({ ...this.#state, errorsPhase: 'error', errorsMessage: message(error, 'Recent errors are unavailable.') })
    }
  }

  /** Saves from the editor; the editor shows a refusal next to the form. */
  saveRequestRule(rule: RequestRule): Promise<MutationResult> {
    return this.#write(rule.id, () => this.#port.saveRequestRule(rule), 'The rule could not be saved.')
  }

  saveRetryRule(rule: RetryRule): Promise<MutationResult> {
    return this.#write(rule.id, () => this.#port.saveRetryRule(rule), 'The rule could not be saved.')
  }

  async setRequestRuleEnabled(rule: RequestRule, enabled: boolean): Promise<boolean> {
    return this.#listChange(rule.id, () => this.#port.saveRequestRule({ ...rule, enabled }), 'The rule could not be changed.')
  }

  async setRetryRuleEnabled(rule: RetryRule, enabled: boolean): Promise<boolean> {
    return this.#listChange(rule.id, () => this.#port.saveRetryRule({ ...rule, enabled }), 'The rule could not be changed.')
  }

  async deleteRequestRule(id: string): Promise<boolean> {
    return this.#listChange(id, () => this.#port.deleteRequestRule(id), 'The rule could not be deleted.')
  }

  async deleteRetryRule(id: string): Promise<boolean> {
    return this.#listChange(id, () => this.#port.deleteRetryRule(id), 'The rule could not be deleted.')
  }

  async moveRequestRule(id: string, direction: -1 | 1): Promise<boolean> {
    const ids = this.#state.requestRules.map((rule) => rule.id)
    const next = moveId(ids, id, direction)
    if (next === ids) return false
    return this.#listChange('*', () => this.#port.reorderRequestRules(next), 'The rules could not be reordered.')
  }

  async moveRetryRule(id: string, direction: -1 | 1): Promise<boolean> {
    const ids = this.#state.retryRules.map((rule) => rule.id)
    const next = moveId(ids, id, direction)
    if (next === ids) return false
    return this.#listChange('*', () => this.#port.reorderRetryRules(next), 'The rules could not be reordered.')
  }

  /** Which rules a request or an error would match. Nothing is sent upstream. */
  test(input: RuleTestInput, signal?: AbortSignal): Promise<RuleTestResult> {
    return this.#port.test(input, signal)
  }

  clearError(): void {
    if (this.#state.error) this.#set({ ...this.#state, error: '' })
  }

  dispose(): void {
    this.#unsubscribe?.()
    this.#unsubscribe = null
    this.#listeners.clear()
  }

  async #refresh(): Promise<void> {
    await this.load()
    if (this.#state.errorsPhase === 'ready') await this.loadErrors()
  }

  async #write(pending: string, operation: () => Promise<unknown>, fallback: string): Promise<MutationResult> {
    if (this.#state.pending) return { ok: false, message: 'Another change is still being saved.' }
    this.#set({ ...this.#state, pending, error: '' })
    try {
      await operation()
      await this.#refresh()
      return { ok: true }
    } catch (error) {
      return { ok: false, message: message(error, fallback) }
    } finally {
      this.#set({ ...this.#state, pending: '' })
    }
  }

  async #listChange(pending: string, operation: () => Promise<unknown>, fallback: string): Promise<boolean> {
    const result = await this.#write(pending, operation, fallback)
    if (!result.ok) {
      // A refused change may mean the list moved on: show the current one.
      await this.load()
      this.#set({ ...this.#state, error: result.message })
    }
    return result.ok
  }

  #set(state: RulesState): void {
    this.#state = state
    for (const listener of this.#listeners) listener()
  }
}
