import type { AddApiKey, ApiKey, UpdateApiKey } from '../domain/api-key'
import type { ApiKeysPort } from './api-keys-port'
import { ControlPlaneError } from '../../../shared/contracts/protocol'

export interface ApiKeysState {
  readonly phase: 'idle' | 'loading' | 'ready' | 'error'
  readonly providerId: string
  readonly keys: readonly ApiKey[]
  readonly pendingId: string
  readonly error: string
}

export class ApiKeysModel {
  readonly #port: ApiKeysPort
  #state: ApiKeysState = { phase: 'idle', providerId: '', keys: [], pendingId: '', error: '' }
  #listeners = new Set<() => void>()
  #unsubscribe: (() => void) | null = null
  #generation = 0

  constructor(port: ApiKeysPort) {
    this.#port = port
    this.#unsubscribe = port.subscribe((providerId) => {
      if (providerId === this.#state.providerId) void this.load(providerId)
    })
  }

  snapshot = (): ApiKeysState => this.#state

  subscribe = (listener: () => void): (() => void) => {
    this.#listeners.add(listener)
    return () => this.#listeners.delete(listener)
  }

  async load(providerId: string, clearPending = false): Promise<void> {
    if (!providerId) return
    const generation = ++this.#generation
    this.#set({ ...this.#state, phase: 'loading', providerId, error: '' })
    try {
      const keys = await this.#port.list(providerId)
      if (generation !== this.#generation) return
      this.#set({ phase: 'ready', providerId, keys, pendingId: clearPending ? '' : this.#state.pendingId, error: '' })
    } catch {
      if (generation !== this.#generation) return
      this.#set({ ...this.#state, phase: 'error', pendingId: clearPending ? '' : this.#state.pendingId, error: 'API keys are unavailable' })
    }
  }

  async add(value: AddApiKey): Promise<boolean> {
    return this.#mutate('new', () => this.#port.add(value))
  }

  async update(value: UpdateApiKey): Promise<boolean> {
    return this.#mutate(value.keyId, () => this.#port.update(value))
  }

  async remove(keyId: string): Promise<boolean> {
    return this.#mutate(keyId, () => this.#port.remove(this.#state.providerId, keyId))
  }

  async move(keyId: string, direction: -1 | 1): Promise<boolean> {
    return this.#mutate(keyId, () => this.#port.move(this.#state.providerId, keyId, direction))
  }

  async reset(keyId: string): Promise<boolean> {
    return this.#mutate(keyId, () => this.#port.reset(this.#state.providerId, keyId))
  }

  clearError(): void {
    if (this.#state.error) this.#set({ ...this.#state, error: '' })
  }

  dispose(): void {
    this.#unsubscribe?.()
    this.#listeners.clear()
  }

  async #mutate(pendingId: string, operation: () => Promise<unknown>): Promise<boolean> {
    if (this.#state.pendingId) return false
    this.#set({ ...this.#state, pendingId, error: '' })
    try {
      await operation()
      await this.load(this.#state.providerId, true)
      return true
    } catch (error) {
      this.#set({ ...this.#state, pendingId: '', error: error instanceof ControlPlaneError ? error.message : 'Key settings could not be saved' })
      return false
    }
  }

  #set(state: ApiKeysState): void {
    this.#state = state
    for (const listener of this.#listeners) listener()
  }
}
