import type { ModelTestResult } from '../domain/model'
import type { ModelsPort } from './models-port'
import { ControlPlaneError } from '../../../shared/contracts/protocol'

const MODEL_TEST_BATCH = 500
const MODEL_TEST_RUN_TIMEOUT_MS = 2 * 60_000

export interface ModelsState {
  readonly phase: 'idle' | 'loading' | 'ready' | 'error'
  readonly providerId: string
  readonly models: readonly string[]
  readonly selected: readonly string[]
  readonly results: Readonly<Record<string, ModelTestResult>>
  readonly testing: boolean
  readonly error: string
}

export class ModelsModel {
  readonly #port: ModelsPort
  #state: ModelsState = { phase: 'idle', providerId: '', models: [], selected: [], results: resultMap(), testing: false, error: '' }
  #listeners = new Set<() => void>()
  #unsubscribe: (() => void) | null = null
  #discoverController: AbortController | null = null
  #testController: AbortController | null = null
  #resultTimer: ReturnType<typeof setTimeout> | undefined
  #pendingResults = new Map<string, ModelTestResult>()
  #generation = 0
  #activeRun = ''

  constructor(port: ModelsPort) {
    this.#port = port
  }

  snapshot = () => this.#state
  subscribe = (listener: () => void) => { this.#listeners.add(listener); return () => this.#listeners.delete(listener) }

  connect() {
    this.#unsubscribe ??= this.#port.subscribe((result) => this.#accept(result))
  }

  async discover(providerId: string) {
    this.#discoverController?.abort()
    this.#testController?.abort()
    this.#testController = null
    this.#activeRun = ''
    clearTimeout(this.#resultTimer)
    this.#resultTimer = undefined
    this.#pendingResults.clear()
    const controller = new AbortController()
    this.#discoverController = controller
    const generation = ++this.#generation
    this.#set({ ...this.#state, phase: 'loading', providerId, models: [], selected: [], results: resultMap(), testing: false, error: '' })
    try {
      const models = await this.#port.discover(providerId, controller.signal)
      if (generation === this.#generation) this.#set({ ...this.#state, phase: 'ready', providerId, models, selected: [], results: resultMap(), error: '' })
    } catch (error) {
      // The control plane already knows whether the key was refused, the catalog was
      // too large or the path was wrong; repeating one guess here hid all three.
      if (generation === this.#generation) this.#set({ ...this.#state, phase: 'error', models: [], selected: [], error: error instanceof ControlPlaneError ? error.message : 'Provider model catalog is unavailable. Check the provider address and its key.' })
    } finally {
      if (this.#discoverController === controller) this.#discoverController = null
    }
  }

  toggle(model: string) {
    const selected = this.#state.selected.includes(model)
      ? this.#state.selected.filter((item) => item !== model)
      : [...this.#state.selected, model]
    this.#set({ ...this.#state, selected })
  }

  toggleAll() {
    this.#set({ ...this.#state, selected: this.#state.selected.length === this.#state.models.length ? [] : [...this.#state.models] })
  }

  /** Replaces the selection with the discovered models of the given list. */
  select(models: readonly string[]) {
    const discovered = new Set(this.#state.models)
    const selected = models.filter((model) => discovered.has(model))
    if (selected.length === this.#state.selected.length && selected.every((model, index) => model === this.#state.selected[index])) return
    this.#set({ ...this.#state, selected })
  }

  async test(models: readonly string[]) {
    if (this.#state.testing || models.length === 0) return false
    const providerId = this.#state.providerId
    const generation = this.#generation
    const runId = `models_${crypto.randomUUID().replaceAll('-', '')}`
    this.#activeRun = runId
    const controller = new AbortController()
    let timedOut = false
    const runTimer = setTimeout(() => { timedOut = true; controller.abort() }, MODEL_TEST_RUN_TIMEOUT_MS)
    this.#testController = controller
    const results = resultMap(this.#state.results)
    for (const model of models) results[model] = { runId, providerId, model, state: 'testing', status: 0, latencyMs: 0 }
    this.#set({ ...this.#state, testing: true, results, error: '' })
    try {
      for (let offset = 0; offset < models.length; offset += MODEL_TEST_BATCH) {
        const batch = models.slice(offset, offset + MODEL_TEST_BATCH)
        if (await this.#port.test(providerId, runId, batch, controller.signal) !== batch.length) throw new Error('incomplete model test batch')
        if (generation !== this.#generation || this.#state.providerId !== providerId) return false
        this.#flushResults()
      }
      this.#set({ ...this.#state, testing: false, results: this.#settle(models, providerId, 'result_missing') })
      return true
    } catch {
      if (generation !== this.#generation || this.#state.providerId !== providerId) return false
      this.#flushResults()
      this.#set({ ...this.#state, testing: false, results: this.#settle(models, providerId, timedOut ? 'timeout' : 'interrupted'), error: timedOut ? 'Model tests reached the 2 minute safety limit' : 'Model tests were interrupted' })
      return false
    } finally {
      clearTimeout(runTimer)
      if (this.#testController === controller) this.#testController = null
      if (this.#activeRun === runId) this.#activeRun = ''
    }
  }

  cancelTest() { this.#activeRun = ''; this.#testController?.abort() }

  dispose() {
    this.#generation++
    this.#activeRun = ''
    this.#discoverController?.abort()
    this.#testController?.abort()
    clearTimeout(this.#resultTimer)
    this.#pendingResults.clear()
    this.#unsubscribe?.()
    this.#listeners.clear()
  }

  #accept(result: ModelTestResult) {
    if (result.providerId !== this.#state.providerId || result.runId !== this.#activeRun) return
    this.#pendingResults.set(result.model, result)
    this.#resultTimer ??= setTimeout(() => this.#flushResults(), 16)
  }

  #flushResults() {
    clearTimeout(this.#resultTimer)
    this.#resultTimer = undefined
    if (this.#pendingResults.size === 0) return
    const results = resultMap(this.#state.results)
    for (const [model, result] of this.#pendingResults) {
      if (result.providerId === this.#state.providerId) results[model] = result
    }
    this.#pendingResults.clear()
    this.#set({ ...this.#state, results })
  }

  #settle(models: readonly string[], providerId: string, errorCode: string) {
    const results = resultMap(this.#state.results)
    if (this.#state.providerId !== providerId) return results
    for (const model of models) {
      if (results[model]?.state === 'testing') results[model] = { runId: results[model].runId, providerId, model, state: 'unavailable', status: 0, latencyMs: 0, errorCode }
    }
    return results
  }

  #set(state: ModelsState) {
    this.#state = state
    for (const listener of this.#listeners) listener()
  }
}

function resultMap(source?: Readonly<Record<string, ModelTestResult>>): Record<string, ModelTestResult> {
  return Object.assign(Object.create(null) as Record<string, ModelTestResult>, source)
}
