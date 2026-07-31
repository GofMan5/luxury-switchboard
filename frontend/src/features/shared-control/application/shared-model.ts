import type { SharedAction, SharedSnapshot } from '../domain/snapshot'
import type { SharedPort } from './shared-port'

const empty: SharedSnapshot = { available: false, revision: 0, tunnels: [], error: '' }

export interface SharedState {
  readonly snapshot: SharedSnapshot
  readonly refreshing: boolean
  readonly pendingPosition: number | null
  readonly error: string
}

export class SharedModel {
  readonly #port: SharedPort
  #state: SharedState = { snapshot: empty, refreshing: false, pendingPosition: null, error: '' }
  #listeners = new Set<() => void>()
  #timer: ReturnType<typeof setInterval> | undefined
  #inFlight = false

  constructor(port: SharedPort) { this.#port = port }
  snapshot = () => this.#state
  subscribe = (listener: () => void) => { this.#listeners.add(listener); return () => this.#listeners.delete(listener) }

  async connect() {
    await this.refresh()
    this.#timer ??= setInterval(() => void this.refresh(), 5_000)
  }

  async refresh() {
    if (this.#inFlight) return
    this.#inFlight = true
    this.#set({ ...this.#state, refreshing: true })
    try {
      const snapshot = await this.#port.list()
      this.#set({ ...this.#state, snapshot, refreshing: false, error: snapshot.error })
    } catch {
      this.#set({ ...this.#state, refreshing: false, error: 'Shared tunnel control unavailable' })
    } finally {
      this.#inFlight = false
    }
  }

  async control(position: number, action: SharedAction) {
    if (this.#state.pendingPosition !== null) return false
    this.#set({ ...this.#state, pendingPosition: position, error: '' })
    try {
      const snapshot = await this.#port.control(position, this.#state.snapshot.revision, action)
      this.#set({ snapshot, refreshing: false, pendingPosition: null, error: snapshot.error })
      return true
    } catch {
      this.#set({ ...this.#state, pendingPosition: null, error: 'Shared tunnel control unavailable' })
      await this.refresh()
      return false
    }
  }

  dispose() {
    clearInterval(this.#timer)
    this.#listeners.clear()
  }

  #set(state: SharedState) {
    this.#state = state
    for (const listener of this.#listeners) listener()
  }
}
