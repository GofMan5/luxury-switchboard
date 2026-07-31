import type { RelaySnapshot } from '../domain/relay'
import type { RelayPort } from './relay-port'

export interface RelayModelState {
  readonly phase: 'loading' | 'ready' | 'error'
  readonly snapshot: RelaySnapshot
  readonly pending: boolean
  readonly error: string
}

const initialState: RelayModelState = {
  phase: 'loading',
  snapshot: { state: 'starting', address: '', port: 0 },
  pending: false,
  error: '',
}

export class RelayModel {
  readonly #port: RelayPort
  #state = initialState
  #listeners = new Set<() => void>()
  #unsubscribe: (() => void) | null = null
  #generation = 0

  constructor(port: RelayPort) {
    this.#port = port
  }

  snapshot = (): RelayModelState => this.#state

  subscribe = (listener: () => void): (() => void) => {
    this.#listeners.add(listener)
    return () => this.#listeners.delete(listener)
  }

  async connect(): Promise<void> {
    const generation = ++this.#generation
    this.#unsubscribe ??= this.#port.subscribe((snapshot) => {
      this.#generation++
      this.#set({ phase: 'ready', snapshot, pending: this.#state.pending, error: '' })
    })
    try {
      const snapshot = await this.#port.status()
      if (generation === this.#generation) this.#set({ phase: 'ready', snapshot, pending: false, error: '' })
    } catch {
      if (generation === this.#generation) this.#set({ ...this.#state, phase: 'error', error: 'Relay state is unavailable' })
    }
  }

  async toggle(): Promise<void> {
    if (this.#state.pending) return
    this.#set({ ...this.#state, pending: true, error: '' })
    try {
      const snapshot =
        this.#state.snapshot.state === 'live' ? await this.#port.stop() : await this.#port.start()
      this.#set({ phase: 'ready', snapshot, pending: false, error: '' })
    } catch {
      try {
        const snapshot = await this.#port.status()
        this.#set({ phase: 'ready', snapshot, pending: false, error: '' })
      } catch {
        this.#set({ ...this.#state, pending: false, error: 'Relay operation failed' })
      }
    }
  }

  dispose(): void {
    this.#unsubscribe?.()
    this.#unsubscribe = null
    this.#listeners.clear()
  }

  #set(state: RelayModelState): void {
    this.#state = state
    for (const listener of this.#listeners) listener()
  }
}
