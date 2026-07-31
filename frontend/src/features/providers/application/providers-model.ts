import type { ProviderCatalog } from '../domain/provider'
import type { ProviderInput } from '../domain/provider'
import type { ProvidersPort } from './providers-port'

export interface ProvidersModelState {
  readonly phase: 'loading' | 'ready' | 'error'
  readonly catalog: ProviderCatalog
  readonly pendingId: string
  readonly error: string
}

const initialState: ProvidersModelState = {
  phase: 'loading',
  catalog: { activeId: '', providers: [] },
  pendingId: '',
  error: '',
}

export class ProvidersModel {
  readonly #port: ProvidersPort
  #state = initialState
  #listeners = new Set<() => void>()
  #unsubscribe: (() => void) | null = null

  constructor(port: ProvidersPort) {
    this.#port = port
  }

  snapshot = (): ProvidersModelState => this.#state

  subscribe = (listener: () => void): (() => void) => {
    this.#listeners.add(listener)
    return () => this.#listeners.delete(listener)
  }

  async connect(): Promise<void> {
    this.#unsubscribe ??= this.#port.subscribe(() => void this.refresh())
    await this.refresh()
  }

  async refresh(): Promise<void> {
    try {
      const catalog = await this.#port.list()
      this.#set({ phase: 'ready', catalog, pendingId: '', error: '' })
    } catch {
      this.#set({ ...this.#state, phase: 'error', pendingId: '', error: 'Providers are unavailable' })
    }
  }

  async activate(id: string): Promise<void> {
    if (this.#state.pendingId || id === this.#state.catalog.activeId) return
    this.#set({ ...this.#state, pendingId: id, error: '' })
    try {
      await this.#port.activate(id)
      await this.refresh()
    } catch {
      this.#set({ ...this.#state, pendingId: '', error: 'Provider could not be activated' })
    }
  }

  async add(value: ProviderInput): Promise<boolean> {
    return this.#mutate('new', () => this.#port.add(value))
  }

  async update(id: string, value: ProviderInput): Promise<boolean> {
    return this.#mutate(id, () => this.#port.update(id, value))
  }

  async delete(id: string): Promise<boolean> {
    return this.#mutate(id, () => this.#port.delete(id))
  }

  dispose(): void {
    this.#unsubscribe?.()
    this.#unsubscribe = null
    this.#listeners.clear()
  }

  #set(state: ProvidersModelState): void {
    this.#state = state
    for (const listener of this.#listeners) listener()
  }

  async #mutate(pendingId: string, operation: () => Promise<unknown>): Promise<boolean> {
    if (this.#state.pendingId) return false
    this.#set({ ...this.#state, pendingId, error: '' })
    try {
      await operation()
      await this.refresh()
      return true
    } catch {
      this.#set({ ...this.#state, pendingId: '', error: 'Provider settings could not be saved' })
      return false
    }
  }
}
