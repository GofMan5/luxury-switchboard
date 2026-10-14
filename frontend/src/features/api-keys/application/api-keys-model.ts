import type { AddApiKey, ApiKey, ImportApiKeys, ImportApiKeysReport, UpdateApiKey } from '../domain/api-key'
import type { PoolCheckReport } from './api-keys-port'
import type { ApiKeysPort } from './api-keys-port'
import { ControlPlaneError } from '../../../shared/contracts/protocol'

export interface ApiKeysState {
  readonly phase: 'idle' | 'loading' | 'ready' | 'error'
  readonly providerId: string
  readonly keys: readonly ApiKey[]
  readonly available: number
  readonly pendingId: string
  readonly error: string
  /** A pool check is running: the button shows it and the pool stays usable. */
  readonly checkingPool: boolean
  /** The last pool check's verdict, kept until the next one or a provider switch. */
  readonly poolReport: PoolCheckReport | null
}

export class ApiKeysModel {
  readonly #port: ApiKeysPort
  #state: ApiKeysState = { phase: 'idle', providerId: '', keys: [], available: 0, pendingId: '', error: '', checkingPool: false, poolReport: null }
  #listeners = new Set<() => void>()
  #unsubscribe: (() => void) | null = null
  #generation = 0
  #poolGeneration = 0
  #poolController: AbortController | null = null
  #mutationGeneration = 0
  #mutationController: AbortController | null = null
  #disposed = false
  #listController: AbortController | null = null
  #background = false
  #refreshTimer: ReturnType<typeof setTimeout> | undefined
  #refreshDelay = 2_000

  constructor(port: ApiKeysPort) {
    this.#port = port
    this.#unsubscribe = port.subscribe((providerId) => {
      if (providerId === this.#state.providerId) void this.load(providerId)
    })
  }

  snapshot = (): ApiKeysState => this.#state

  subscribe = (listener: () => void): (() => void) => {
    if (this.#disposed) return () => undefined
    this.#listeners.add(listener)
    this.#scheduleRefresh()
    return () => {
      this.#listeners.delete(listener)
      if (this.#listeners.size === 0) {
        clearTimeout(this.#refreshTimer)
        this.#refreshTimer = undefined
        if (this.#background) {
          this.#generation++
          this.#listController?.abort()
          this.#listController = null
          this.#background = false
        }
      }
    }
  }

  async load(providerId: string): Promise<void> {
    await this.#load(providerId, false)
  }

  async #load(providerId: string, background: boolean): Promise<void> {
    if (!providerId || this.#disposed) return
    clearTimeout(this.#refreshTimer)
    this.#refreshTimer = undefined
    this.#listController?.abort()
    const controller = new AbortController()
    this.#listController = controller
    this.#background = background
    const generation = ++this.#generation
    const sameProvider = providerId === this.#state.providerId
    if (!sameProvider) {
      this.#poolGeneration++
      this.#poolController?.abort()
      this.#poolController = null
      this.#mutationGeneration++
      this.#mutationController?.abort()
    }
    const poolReport = sameProvider ? this.#state.poolReport : null
    if (!background) this.#set({ ...this.#state, phase: sameProvider && this.#state.phase === 'ready' ? 'ready' : 'loading', providerId, keys: sameProvider ? this.#state.keys : [], available: sameProvider ? this.#state.available : 0, pendingId: sameProvider ? this.#state.pendingId : '', poolReport, checkingPool: sameProvider && this.#state.checkingPool, error: '' })
    try {
      const { keys, available } = await this.#port.list(providerId, controller.signal)
      if (generation !== this.#generation) return
      this.#refreshDelay = 2_000
      this.#set({ ...this.#state, phase: 'ready', providerId, keys, available: Math.max(available, keys.length), error: background ? this.#state.error : '' })
    } catch (error) {
      if (generation !== this.#generation) return
      this.#refreshDelay = Math.min(this.#refreshDelay * 2, 30_000)
      if (!background || error instanceof ControlPlaneError) this.#set({ ...this.#state, phase: background ? this.#state.phase : 'error', error: error instanceof ControlPlaneError ? error.message : 'API keys are unavailable' })
    } finally {
      if (this.#listController === controller) {
        this.#listController = null
        this.#background = false
        this.#scheduleRefresh()
      }
    }
  }

  /** Probes every key of the current provider through its own catalog
   * endpoint: rejected credentials earn their streak, accepted ones reset it,
   * and the report says what the round found. The pool stays usable while it
   * runs. */
  async checkPool(): Promise<void> {
    if (!this.#state.providerId || this.#state.checkingPool || this.#disposed) return
    const providerId = this.#state.providerId
    const generation = ++this.#poolGeneration
    const controller = new AbortController()
    this.#poolController = controller
    this.#set({ ...this.#state, checkingPool: true, error: '' })
    try {
      const poolReport = await this.#port.checkPool(providerId, controller.signal)
      if (generation !== this.#poolGeneration) return
      await this.load(providerId)
      if (generation !== this.#poolGeneration) return
      this.#set({ ...this.#state, poolReport, checkingPool: false })
    } catch (error) {
      if (generation !== this.#poolGeneration) return
      this.#set({ ...this.#state, checkingPool: false, error: error instanceof ControlPlaneError ? error.message : 'The pool could not be checked' })
    } finally {
      if (this.#poolController === controller) this.#poolController = null
    }
  }

  /** Removes every key the last check marked rejected. Sequential on purpose:
   * each removal is its own save, so a failure leaves the rest in place. */
  async removeRejected(): Promise<void> {
    const rejected = this.#state.keys.filter((key) => key.authStreak >= 3 && !key.pinned)
    for (const key of rejected) {
      if (!await this.remove(key.id)) return
    }
  }

  async add(value: AddApiKey): Promise<boolean> {
    return this.#mutate('new', (signal) => this.#port.add(value, signal))
  }

  /**
   * Imports pasted keys and returns what became of them, so the caller can say how
   * many were added and which were skipped. Null means the import did not run.
   */
  async importKeys(value: ImportApiKeys): Promise<ImportApiKeysReport | null> {
    if (value.entries.length === 0) return null
    return this.#run('import', (signal) => this.#port.addMany(value, signal))
  }

  async update(value: UpdateApiKey): Promise<boolean> {
    return this.#mutate(value.keyId, (signal) => this.#port.update(value, signal))
  }

  async remove(keyId: string): Promise<boolean> {
    return this.#mutate(keyId, (signal) => this.#port.remove(this.#state.providerId, keyId, signal))
  }

  async move(keyId: string, direction: -1 | 1): Promise<boolean> {
    return this.#mutate(keyId, (signal) => this.#port.move(this.#state.providerId, keyId, direction, signal))
  }

  async reset(keyId: string): Promise<boolean> {
    return this.#mutate(keyId, (signal) => this.#port.reset(this.#state.providerId, keyId, signal))
  }

  clearError(): void {
    if (this.#state.error) this.#set({ ...this.#state, error: '' })
  }

  dispose(): void {
    this.#disposed = true
    clearTimeout(this.#refreshTimer)
    this.#refreshTimer = undefined
    this.#listController?.abort()
    this.#listController = null
    this.#mutationGeneration++
    this.#mutationController?.abort()
    this.#poolGeneration++
    this.#poolController?.abort()
    this.#poolController = null
    this.#generation++
    this.#unsubscribe?.()
    this.#listeners.clear()
  }

  async #mutate(pendingId: string, operation: (signal: AbortSignal) => Promise<unknown>): Promise<boolean> {
    return await this.#run(pendingId, async (signal) => { await operation(signal); return true }) === true
  }

  async #run<Result>(pendingId: string, operation: (signal: AbortSignal) => Promise<Result>): Promise<Result | null> {
    if (this.#state.pendingId || this.#disposed) return null
    const generation = ++this.#mutationGeneration
    const providerId = this.#state.providerId
    const controller = new AbortController()
    this.#mutationController = controller
    this.#set({ ...this.#state, pendingId, error: '' })
    try {
      const result = await operation(controller.signal)
      if (generation !== this.#mutationGeneration) return null
      await this.load(providerId)
      if (generation !== this.#mutationGeneration) return null
      this.#set({ ...this.#state, pendingId: '' })
      return result
    } catch (error) {
      if (generation !== this.#mutationGeneration) return null
      this.#set({ ...this.#state, pendingId: '', error: error instanceof ControlPlaneError ? error.message : 'Key settings could not be saved' })
      return null
    } finally {
      if (this.#mutationController === controller) this.#mutationController = null
    }
  }

  #scheduleRefresh(): void {
    if (this.#disposed || this.#listeners.size === 0 || !this.#state.providerId || this.#listController || this.#refreshTimer !== undefined) return
    this.#refreshTimer = setTimeout(() => {
      this.#refreshTimer = undefined
      void this.#load(this.#state.providerId, true)
    }, this.#refreshDelay)
  }

  #set(state: ApiKeysState): void {
    this.#state = state
    for (const listener of this.#listeners) listener()
  }
}
