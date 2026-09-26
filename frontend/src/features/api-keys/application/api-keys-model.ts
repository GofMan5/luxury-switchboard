import type { AddApiKey, ApiKey, ImportApiKeys, ImportApiKeysReport, UpdateApiKey } from '../domain/api-key'
import type { PoolCheckReport } from './api-keys-port'
import type { ApiKeysPort } from './api-keys-port'
import { ControlPlaneError } from '../../../shared/contracts/protocol'

export interface ApiKeysState {
  readonly phase: 'idle' | 'loading' | 'ready' | 'error'
  readonly providerId: string
  readonly keys: readonly ApiKey[]
  readonly pendingId: string
  readonly error: string
  /** A pool check is running: the button shows it and the pool stays usable. */
  readonly checkingPool: boolean
  /** The last pool check's verdict, kept until the next one or a provider switch. */
  readonly poolReport: PoolCheckReport | null
}

export class ApiKeysModel {
  readonly #port: ApiKeysPort
  #state: ApiKeysState = { phase: 'idle', providerId: '', keys: [], pendingId: '', error: '', checkingPool: false, poolReport: null }
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
    const poolReport = providerId === this.#state.providerId ? this.#state.poolReport : null
    this.#set({ ...this.#state, phase: 'loading', providerId, keys: providerId === this.#state.providerId ? this.#state.keys : [], poolReport, error: '' })
    try {
      const keys = await this.#port.list(providerId)
      if (generation !== this.#generation) return
      this.#set({ ...this.#state, phase: 'ready', providerId, keys, pendingId: clearPending ? '' : this.#state.pendingId, error: '' })
    } catch {
      if (generation !== this.#generation) return
      this.#set({ ...this.#state, phase: 'error', pendingId: clearPending ? '' : this.#state.pendingId, error: 'API keys are unavailable' })
    }
  }

  /** Probes every key of the current provider through its own catalog
   * endpoint: rejected credentials earn their streak, accepted ones reset it,
   * and the report says what the round found. The pool stays usable while it
   * runs. */
  async checkPool(): Promise<void> {
    if (!this.#state.providerId || this.#state.checkingPool) return
    this.#set({ ...this.#state, checkingPool: true, error: '' })
    try {
      const poolReport = await this.#port.checkPool(this.#state.providerId)
      const keys = await this.#port.list(this.#state.providerId)
      this.#set({ ...this.#state, phase: 'ready', keys, poolReport, checkingPool: false })
    } catch (error) {
      this.#set({ ...this.#state, checkingPool: false, error: error instanceof ControlPlaneError ? error.message : 'The pool could not be checked' })
    }
  }

  /** Removes every key the last check marked rejected. Sequential on purpose:
   * each removal is its own save, so a failure leaves the rest in place. */
  async removeRejected(): Promise<void> {
    const rejected = this.#state.keys.filter((key) => key.authStreak >= 3)
    for (const key of rejected) {
      if (!await this.remove(key.id)) return
    }
  }

  async add(value: AddApiKey): Promise<boolean> {
    return this.#mutate('new', () => this.#port.add(value))
  }

  /**
   * Imports pasted keys and returns what became of them, so the caller can say how
   * many were added and which were skipped. Null means the import did not run.
   */
  async importKeys(value: ImportApiKeys): Promise<ImportApiKeysReport | null> {
    if (value.entries.length === 0) return null
    return this.#run('import', () => this.#port.addMany(value))
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
    return await this.#run(pendingId, async () => { await operation(); return true }) === true
  }

  async #run<Result>(pendingId: string, operation: () => Promise<Result>): Promise<Result | null> {
    if (this.#state.pendingId) return null
    this.#set({ ...this.#state, pendingId, error: '' })
    try {
      const result = await operation()
      await this.load(this.#state.providerId, true)
      return result
    } catch (error) {
      this.#set({ ...this.#state, pendingId: '', error: error instanceof ControlPlaneError ? error.message : 'Key settings could not be saved' })
      return null
    }
  }

  #set(state: ApiKeysState): void {
    this.#state = state
    for (const listener of this.#listeners) listener()
  }
}
