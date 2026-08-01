import type { ModelTestResult } from '../domain/model'
import type { ModelsPort } from './models-port'

const MODEL_TEST_BATCH = 500

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
  #state: ModelsState = { phase: 'idle', providerId: '', models: [], selected: [], results: {}, testing: false, error: '' }
  #listeners = new Set<() => void>()
  #unsubscribe: (() => void) | null = null
  #discoverController: AbortController | null = null
  #testController: AbortController | null = null
  #resultTimer: ReturnType<typeof setTimeout> | undefined
  #pendingResults = new Map<string, ModelTestResult>()
  #generation = 0

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
    clearTimeout(this.#resultTimer)
    this.#resultTimer = undefined
    this.#pendingResults.clear()
    const controller = new AbortController()
    this.#discoverController = controller
    const generation = ++this.#generation
    this.#set({ ...this.#state, phase: 'loading', providerId, models: [], selected: [], results: {}, testing: false, error: '' })
    try {
      const models = await this.#port.discover(providerId, controller.signal)
      if (generation === this.#generation) this.#set({ ...this.#state, phase: 'ready', providerId, models, selected: [], results: {}, error: '' })
    } catch {
      if (generation === this.#generation) this.#set({ ...this.#state, phase: 'error', models: [], selected: [], error: 'Provider model catalog is unavailable' })
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

  async test(models: readonly string[]) {
    if (this.#state.testing || models.length === 0) return false
    const providerId = this.#state.providerId
    const controller = new AbortController()
    this.#testController = controller
    const results = { ...this.#state.results }
    for (const model of models) results[model] = { providerId, model, state: 'testing', status: 0, latencyMs: 0 }
    this.#set({ ...this.#state, testing: true, results, error: '' })
    try {
      for (let offset = 0; offset < models.length; offset += MODEL_TEST_BATCH) {
        const batch = models.slice(offset, offset + MODEL_TEST_BATCH)
        if (await this.#port.test(providerId, batch, controller.signal) !== batch.length) throw new Error('incomplete model test batch')
        this.#flushResults()
      }
      this.#set({ ...this.#state, testing: false, results: this.#settle(models, providerId, 'result_missing') })
      return true
    } catch {
      this.#flushResults()
      const sameProvider = this.#state.providerId === providerId
      this.#set({ ...this.#state, testing: false, results: this.#settle(models, providerId, 'interrupted'), error: sameProvider ? 'Model tests were interrupted' : this.#state.error })
      return false
    } finally {
      if (this.#testController === controller) this.#testController = null
    }
  }

  cancelTest() { this.#testController?.abort() }

  dispose() {
    this.#discoverController?.abort()
    this.#testController?.abort()
    clearTimeout(this.#resultTimer)
    this.#pendingResults.clear()
    this.#unsubscribe?.()
    this.#listeners.clear()
  }

  #accept(result: ModelTestResult) {
    if (result.providerId !== this.#state.providerId) return
    this.#pendingResults.set(result.model, result)
    this.#resultTimer ??= setTimeout(() => this.#flushResults(), 16)
  }

  #flushResults() {
    clearTimeout(this.#resultTimer)
    this.#resultTimer = undefined
    if (this.#pendingResults.size === 0) return
    const results = { ...this.#state.results }
    for (const [model, result] of this.#pendingResults) {
      if (result.providerId === this.#state.providerId) results[model] = result
    }
    this.#pendingResults.clear()
    this.#set({ ...this.#state, results })
  }

  #settle(models: readonly string[], providerId: string, errorCode: string) {
    const results = { ...this.#state.results }
    if (this.#state.providerId !== providerId) return results
    for (const model of models) {
      if (results[model]?.state === 'testing') results[model] = { providerId, model, state: 'unavailable', status: 0, latencyMs: 0, errorCode }
    }
    return results
  }

  #set(state: ModelsState) {
    this.#state = state
    for (const listener of this.#listeners) listener()
  }
}
