import type { ModelTestResult } from '../../models/domain/model'
import type { ModelsPort } from '../../models/application/models-port'

const RUN_TIMEOUT_MS = 3 * 60_000

export interface TestTarget {
  readonly providerId: string
  readonly providerName: string
  readonly models: readonly string[]
}

export interface TestsState {
  /** Per provider: the discovered catalog, or 'loading'/'error'. */
  readonly catalogs: Readonly<Record<string, readonly string[] | 'loading' | 'error'>>
  /** Results keyed `${providerId}${model}` — one row per measured pair. */
  readonly results: Readonly<Record<string, ModelTestResult>>
  readonly running: boolean
  /** Live run progress: settled probes of the current run's total. */
  readonly runDone: number
  readonly runTotal: number
  readonly runFailed: number
  /** When the last run finished; empty before the first. */
  readonly lastRunAt: string
  readonly error: string
}

/** The space separates the halves: "ab"+"c" and "a"+"bc" must not collide. */
export function testResultKey(providerId: string, model: string): string {
  return `${providerId} ${model}`
}
const resultKey = testResultKey

/**
 * The Tests workspace measures providers the way the operator feels them:
 * time to first token, total time, tokens per second. It reuses the models
 * port — the backend's test command is the probe — and adds the
 * multi-provider scope the Model Routes test drawer does not have.
 */
export class TestsModel {
  readonly #port: ModelsPort
  #state: TestsState = { catalogs: {}, results: {}, running: false, runDone: 0, runTotal: 0, runFailed: 0, lastRunAt: '', error: '' }
  #listeners = new Set<() => void>()
  #unsubscribe: (() => void) | null = null
  #controller: AbortController | null = null
  #activeRun = ''
  #generation = 0

  constructor(port: ModelsPort) { this.#port = port }
  snapshot = (): TestsState => this.#state
  subscribe = (listener: () => void): (() => void) => { this.#listeners.add(listener); return () => this.#listeners.delete(listener) }

  connect(): void {
    this.#unsubscribe ??= this.#port.subscribe((result) => {
      if (result.runId !== this.#activeRun) return
      this.#set({ ...this.#state, runDone: this.#state.runDone + 1, runFailed: this.#state.runFailed + (result.state === 'available' ? 0 : 1), results: { ...this.#state.results, [resultKey(result.providerId, result.model)]: result } })
    })
  }

  /** Reads a provider's catalog once; 'loading' and 'error' are the honest
   * states, and a cached catalog is not silently re-fetched. */
  async ensureCatalog(providerId: string): Promise<readonly string[]> {
    const known = this.#state.catalogs[providerId]
    if (Array.isArray(known)) return known
    // 'error' is terminal until the operator asks again: an unreadable catalog
    // must not be re-fetched by every render of the page that shows it.
    if (known === 'loading' || known === 'error') return []
    this.#set({ ...this.#state, catalogs: { ...this.#state.catalogs, [providerId]: 'loading' } })
    try {
      const models = await this.#port.discover(providerId)
      this.#set({ ...this.#state, catalogs: { ...this.#state.catalogs, [providerId]: models } })
      return models
    } catch {
      this.#set({ ...this.#state, catalogs: { ...this.#state.catalogs, [providerId]: 'error' } })
      return []
    }
  }

  async refreshCatalog(providerId: string): Promise<readonly string[]> {
    this.#set({ ...this.#state, catalogs: { ...this.#state.catalogs, [providerId]: 'loading' } })
    try {
      const models = await this.#port.discover(providerId)
      this.#set({ ...this.#state, catalogs: { ...this.#state.catalogs, [providerId]: models } })
      return models
    } catch {
      this.#set({ ...this.#state, catalogs: { ...this.#state.catalogs, [providerId]: 'error' } })
      return []
    }
  }

  /**
   * Runs the scope sequentially: the control plane runs one test batch at a
   * time, so parallel calls would only bounce off its lock. Each provider's
   * batch publishes per-model events as they land, and one provider's failure
   * never stops the rest of the run.
   */
  async run(targets: readonly TestTarget[]): Promise<void> {
    if (this.#state.running) return
    const planned = targets.filter((target) => target.models.length > 0)
    if (planned.length === 0) return
    const generation = ++this.#generation
    const runId = `probe_${crypto.randomUUID().replaceAll('-', '')}`
    this.#activeRun = runId
    const controller = new AbortController()
    this.#controller = controller
    let timedOut = false
    const timer = setTimeout(() => { timedOut = true; controller.abort() }, RUN_TIMEOUT_MS * planned.length)
    this.#set({ ...this.#state, running: true, runDone: 0, runFailed: 0, runTotal: planned.reduce((sum, target) => sum + target.models.length, 0), error: '' })
    let failedProviders = 0
    for (const target of planned) {
      if (controller.signal.aborted || generation !== this.#generation) break
      // Rows go testing as their batch starts, not all at once: mid-run the
      // table shows what is actually being measured.
      const results = { ...this.#state.results }
      for (const model of target.models) {
        results[resultKey(target.providerId, model)] = { runId, providerId: target.providerId, model, state: 'testing', status: 0, latencyMs: 0 }
      }
      this.#set({ ...this.#state, results })
      try {
        await this.#port.test(target.providerId, runId, target.models, controller.signal)
        this.#settle([target], runId, 'result_missing')
      } catch {
        // One provider down must not strand the rest of the scope.
        failedProviders++
        this.#settle([target], runId, controller.signal.aborted ? (timedOut ? 'timeout' : 'interrupted') : 'provider_failed')
      }
    }
    clearTimeout(timer)
    if (generation !== this.#generation) return
    this.#set({
      ...this.#state,
      running: false,
      lastRunAt: new Date().toISOString(),
      error: controller.signal.aborted
        ? (timedOut ? 'The run reached the time limit; unfinished models are marked Timeout' : '')
        : failedProviders > 0 ? `${failedProviders} provider${failedProviders === 1 ? '' : 's'} could not be reached — the rest were measured` : '',
    })
    if (this.#controller === controller) this.#controller = null
    if (this.#activeRun === runId) this.#activeRun = ''
  }

  cancel(): void {
    this.#activeRun = ''
    this.#controller?.abort()
  }

  /** Models still marked testing when a run ends never reported — the honest
   * answer is an unavailable row, not a spinner that never stops. */
  #settle(targets: readonly TestTarget[], runId: string, errorCode: string): void {
    const results = { ...this.#state.results }
    let settled = 0
    for (const target of targets) {
      for (const model of target.models) {
        const key = resultKey(target.providerId, model)
        if (results[key]?.state === 'testing') {
          results[key] = { runId, providerId: target.providerId, model, state: errorCode === 'timeout' ? 'timeout' : 'unavailable', status: 0, latencyMs: 0, errorCode }
          settled++
        }
      }
    }
    this.#set({ ...this.#state, runDone: this.#state.runDone + settled, runFailed: this.#state.runFailed + settled, results })
  }

  dispose(): void {
    this.#generation++
    this.cancel()
    this.#unsubscribe?.()
    this.#listeners.clear()
  }

  #set(state: TestsState): void {
    this.#state = state
    for (const listener of this.#listeners) listener()
  }
}
