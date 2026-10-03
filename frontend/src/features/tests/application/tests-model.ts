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
  #state: TestsState = { catalogs: {}, results: {}, running: false, error: '' }
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
      this.#set({ ...this.#state, results: { ...this.#state.results, [resultKey(result.providerId, result.model)]: result } })
    })
  }

  /** Reads a provider's catalog once; 'loading' and 'error' are the honest
   * states, and a cached catalog is not silently re-fetched. */
  async ensureCatalog(providerId: string): Promise<readonly string[]> {
    const known = this.#state.catalogs[providerId]
    if (Array.isArray(known)) return known
    if (known === 'loading') return []
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
   * batch publishes per-model events as they land.
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
    const timer = setTimeout(() => controller.abort(), RUN_TIMEOUT_MS * planned.length)
    const results = { ...this.#state.results }
    for (const target of planned) {
      for (const model of target.models) {
        results[resultKey(target.providerId, model)] = { runId, providerId: target.providerId, model, state: 'testing', status: 0, latencyMs: 0 }
      }
    }
    this.#set({ ...this.#state, running: true, results, error: '' })
    try {
      for (const target of planned) {
        if (controller.signal.aborted || generation !== this.#generation) break
        await this.#port.test(target.providerId, runId, target.models, controller.signal)
      }
      this.#settle(planned, runId, 'result_missing')
      this.#set({ ...this.#state, running: false })
    } catch {
      if (generation !== this.#generation) return
      this.#settle(planned, runId, 'interrupted')
      this.#set({ ...this.#state, running: false, error: 'The test run was interrupted' })
    } finally {
      clearTimeout(timer)
      if (this.#controller === controller) this.#controller = null
      if (this.#activeRun === runId) this.#activeRun = ''
    }
  }

  cancel(): void {
    this.#activeRun = ''
    this.#controller?.abort()
  }

  /** Models still marked testing when a run ends never reported — the honest
   * answer is an unavailable row, not a spinner that never stops. */
  #settle(targets: readonly TestTarget[], runId: string, errorCode: string): void {
    const results = { ...this.#state.results }
    for (const target of targets) {
      for (const model of target.models) {
        const key = resultKey(target.providerId, model)
        if (results[key]?.state === 'testing') {
          results[key] = { runId, providerId: target.providerId, model, state: 'unavailable', status: 0, latencyMs: 0, errorCode }
        }
      }
    }
    this.#set({ ...this.#state, results })
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
