import type { ProviderCatalog, ProviderHealth } from '../domain/provider'
import type { ProviderInput } from '../domain/provider'
import type { ProvidersPort } from './providers-port'
import { ControlPlaneError } from '../../../shared/contracts/protocol'

export interface ProvidersModelState {
  readonly phase: 'loading' | 'ready' | 'error'
  readonly catalog: ProviderCatalog
  readonly pendingId: string
  readonly error: string
  /** Provider reachability by id, updated live by the health probe. */
  readonly health: ReadonlyMap<string, ProviderHealth>
}

const initialState: ProvidersModelState = {
  phase: 'loading',
  catalog: { activeId: '', providers: [] },
  pendingId: '',
  error: '',
  health: new Map(),
}

export class ProvidersModel {
  readonly #port: ProvidersPort
  #state = initialState
  #listeners = new Set<() => void>()
  #unsubscribe: (() => void) | null = null
  #unsubscribeHealth: (() => void) | null = null
  #generation = 0

  constructor(port: ProvidersPort) {
    this.#port = port
  }

  snapshot = (): ProvidersModelState => this.#state

  subscribe = (listener: () => void): (() => void) => {
    this.#listeners.add(listener)
    return () => this.#listeners.delete(listener)
  }

  async connect(): Promise<void> {
    this.#unsubscribe ??= this.#port.subscribe(() => void this.refresh(false))
    // The health probe pushes its own transitions; a failed poll is not an
    // error surface — the dots simply stay at their last known state.
    this.#unsubscribeHealth ??= this.#port.subscribeHealth((states) => {
      this.#set({ ...this.#state, health: new Map(states.map((state) => [state.providerId, state])) })
    })
    await this.refresh()
    try {
      const health = await this.#port.health()
      this.#set({ ...this.#state, health: new Map(health.map((state) => [state.providerId, state])) })
    } catch {
      // Health is ambient state, not a workspace: no error surface for it.
    }
  }

  async refresh(clearPending = true): Promise<void> {
    const generation = ++this.#generation
    try {
      const catalog = await this.#port.list()
      if (generation === this.#generation) this.#set({ ...this.#state, phase: 'ready', catalog, pendingId: clearPending ? '' : this.#state.pendingId, error: '' })
    } catch {
      if (generation === this.#generation) this.#set({ ...this.#state, phase: 'error', pendingId: clearPending ? '' : this.#state.pendingId, error: 'Providers are unavailable' })
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

  clearError(): void {
    if (this.#state.error) this.#set({ ...this.#state, error: '' })
  }

  dispose(): void {
    this.#unsubscribe?.()
    this.#unsubscribe = null
    this.#unsubscribeHealth?.()
    this.#unsubscribeHealth = null
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
    } catch (error) {
      this.#set({ ...this.#state, pendingId: '', error: error instanceof ControlPlaneError ? error.message : 'Provider settings could not be saved' })
      return false
    }
  }
}
