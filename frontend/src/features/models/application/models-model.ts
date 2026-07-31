import type { ModelTestResult } from '../domain/model'
import type { ModelsPort } from './models-port'

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
    const generation = ++this.#generation
    this.#set({ ...this.#state, phase: 'loading', providerId, selected: [], results: {}, error: '' })
    try {
      const models = await this.#port.discover(providerId)
      if (generation === this.#generation) this.#set({ ...this.#state, phase: 'ready', providerId, models, selected: [], results: {}, error: '' })
    } catch {
      if (generation === this.#generation) this.#set({ ...this.#state, phase: 'error', models: [], selected: [], error: 'Provider model catalog is unavailable' })
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
    const results = { ...this.#state.results }
    for (const model of models) results[model] = { providerId: this.#state.providerId, model, state: 'testing', status: 0, latencyMs: 0 }
    this.#set({ ...this.#state, testing: true, results, error: '' })
    try {
      await this.#port.test(this.#state.providerId, models)
      this.#set({ ...this.#state, testing: false })
      return true
    } catch {
      this.#set({ ...this.#state, testing: false, error: 'Model tests were interrupted' })
      return false
    }
  }

  dispose() {
    this.#unsubscribe?.()
    this.#listeners.clear()
  }

  #accept(result: ModelTestResult) {
    if (result.providerId !== this.#state.providerId) return
    this.#set({ ...this.#state, results: { ...this.#state.results, [result.model]: result } })
  }

  #set(state: ModelsState) {
    this.#state = state
    for (const listener of this.#listeners) listener()
  }
}
