import type { ModelRoute, RouteTarget } from '../domain/route'
import type { RoutesPort } from './routes-port'

export interface RoutesState {
  readonly phase: 'idle' | 'loading' | 'ready' | 'error'
  readonly target: RouteTarget
  readonly routes: readonly ModelRoute[]
  readonly pending: string
  readonly error: string
}

export class RoutesModel {
  readonly #port: RoutesPort
  #state: RoutesState = { phase: 'idle', target: 'relay', routes: [], pending: '', error: '' }
  #listeners = new Set<() => void>()
  #unsubscribe: (() => void) | null
  #generation = 0

  constructor(port: RoutesPort) {
    this.#port = port
    this.#unsubscribe = port.subscribe((target) => {
      if (target === this.#state.target) void this.load(target)
    })
  }

  snapshot = (): RoutesState => this.#state

  subscribe = (listener: () => void): (() => void) => {
    this.#listeners.add(listener)
    return () => this.#listeners.delete(listener)
  }

  async load(target: RouteTarget): Promise<void> {
    const generation = ++this.#generation
    this.#set({ ...this.#state, phase: 'loading', target, error: '' })
    try {
      const routes = await this.#port.list(target)
      if (generation === this.#generation) {
        this.#set({ phase: 'ready', target, routes, pending: '', error: '' })
      }
    } catch {
      if (generation === this.#generation) {
        this.#set({ ...this.#state, phase: 'error', error: 'Model routes are unavailable' })
      }
    }
  }

  async upsert(route: ModelRoute): Promise<boolean> {
    return this.#mutate(route.publicModel, () => this.#port.upsert(route))
  }

  async upsertMany(routes: readonly ModelRoute[]): Promise<boolean> {
    if (routes.length === 0) return false
    return this.#mutate('*', () => this.#port.upsertMany(routes))
  }

  async delete(publicModel: string): Promise<boolean> {
    return this.#mutate(publicModel, () => this.#port.delete(this.#state.target, publicModel))
  }

  dispose(): void {
    this.#unsubscribe?.()
    this.#listeners.clear()
  }

  async #mutate(pending: string, operation: () => Promise<unknown>): Promise<boolean> {
    if (this.#state.pending) return false
    this.#set({ ...this.#state, pending, error: '' })
    try {
      await operation()
      await this.load(this.#state.target)
      return true
    } catch {
      this.#set({ ...this.#state, pending: '', error: 'Route could not be saved' })
      return false
    }
  }

  #set(state: RoutesState): void {
    this.#state = state
    for (const listener of this.#listeners) listener()
  }
}
