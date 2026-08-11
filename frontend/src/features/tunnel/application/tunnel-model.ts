import type { TunnelConfig, TunnelSnapshot } from '../domain/tunnel'
import type { TunnelPort } from './tunnel-port'

export interface TunnelModelState {
  readonly phase: 'loading' | 'ready' | 'error'
  readonly snapshot: TunnelSnapshot | null
  readonly pending: boolean
  readonly error: string
}

export class TunnelModel {
  readonly #port: TunnelPort
  #state: TunnelModelState = { phase: 'loading', snapshot: null, pending: false, error: '' }
  #listeners = new Set<() => void>()
  #unsubscribe: (() => void) | null = null
  #generation = 0

  constructor(port: TunnelPort) { this.#port = port }
  snapshot = () => this.#state
  subscribe = (listener: () => void) => { this.#listeners.add(listener); return () => this.#listeners.delete(listener) }

  async connect() {
    const generation = ++this.#generation
    this.#unsubscribe ??= this.#port.subscribe((snapshot) => {
      this.#generation++
      this.#set({ phase: 'ready', snapshot, pending: this.#state.pending, error: snapshot.error ?? '' })
    })
    try {
      const snapshot = await this.#port.get()
      if (generation === this.#generation) this.#set({ phase: 'ready', snapshot, pending: false, error: snapshot.error ?? '' })
    } catch {
      if (generation === this.#generation) this.#set({ ...this.#state, phase: 'error', error: 'Tunnel is unavailable' })
    }
  }

  configure(config: TunnelConfig) { return this.#mutate(() => this.#port.configure(config)) }
  start() { return this.#mutate(() => this.#port.start()) }
  stop() { return this.#mutate(() => this.#port.stop()) }

  async rotate() {
    if (this.#state.pending) return ''
    this.#set({ ...this.#state, pending: true, error: '' })
    try {
      const token = await this.#port.rotate()
      await this.connect()
      return token
    } catch {
      this.#set({ ...this.#state, pending: false, error: 'Tunnel token could not be rotated' })
      return ''
    }
  }

  async reveal() {
    try {
      return await this.#port.reveal()
    } catch {
      this.#set({ ...this.#state, error: 'Tunnel access key is unavailable' })
      return ''
    }
  }
  privacyTest(signal?: AbortSignal) { return this.#port.privacyTest(signal) }
  dispose() { this.#unsubscribe?.(); this.#listeners.clear() }

  async #mutate(operation: () => Promise<TunnelSnapshot>) {
    if (this.#state.pending) return false
    this.#generation++
    this.#set({ ...this.#state, pending: true, error: '' })
    try {
      const snapshot = await operation()
      this.#set({ phase: 'ready', snapshot, pending: false, error: snapshot.error ?? '' })
      return true
    } catch {
      let snapshot = this.#state.snapshot
      try { snapshot = await this.#port.get() } catch { /* keep the last safe snapshot */ }
      this.#set({ ...this.#state, snapshot, pending: false, error: snapshot?.error || 'Tunnel operation failed' })
      return false
    }
  }

  #set(state: TunnelModelState) {
    this.#state = state
    for (const listener of this.#listeners) listener()
  }
}
