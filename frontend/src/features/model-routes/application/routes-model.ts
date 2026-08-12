import { selectionChanges, type ModelRoute, type RouteTarget } from '../domain/route'
import type { RoutesPort } from './routes-port'
import { ControlPlaneError } from '../../../shared/contracts/protocol'

const ROUTE_BATCH = 500
const MAX_SELECTION_REMOVALS = 2_000

export interface RoutesState {
  readonly phase: 'idle' | 'loading' | 'ready' | 'error'
  readonly target: RouteTarget
  readonly routes: readonly ModelRoute[]
  /** Routes of both targets, so the catalog can show where a model is published. */
  readonly published: Readonly<Record<RouteTarget, readonly ModelRoute[]>>
  readonly pending: string
  readonly error: string
}

export class RoutesModel {
  readonly #port: RoutesPort
  #state: RoutesState = { phase: 'idle', target: 'relay', routes: [], published: { relay: [], tunnel: [] }, pending: '', error: '' }
  #listeners = new Set<() => void>()
  #unsubscribe: (() => void) | null
  #generation = 0
  readonly #tracksBothTargets: boolean

  /** A build without the public tunnel has a single target and nothing to compare. */
  constructor(port: RoutesPort, options: { readonly tracksBothTargets?: boolean } = {}) {
    this.#port = port
    this.#tracksBothTargets = options.tracksBothTargets ?? true
    this.#unsubscribe = port.subscribe((target) => {
      if (target === this.#state.target) void this.load(target)
    })
  }

  snapshot = (): RoutesState => this.#state

  subscribe = (listener: () => void): (() => void) => {
    this.#listeners.add(listener)
    return () => this.#listeners.delete(listener)
  }

  async load(target: RouteTarget, clearPending = false): Promise<void> {
    const generation = ++this.#generation
    this.#set({ ...this.#state, phase: 'loading', target, routes: target === this.#state.target ? this.#state.routes : [], error: '' })
    try {
      const routes = await this.#port.list(target)
      if (generation === this.#generation) {
        this.#set({
          phase: 'ready', target, routes, published: { ...this.#state.published, [target]: routes },
          pending: clearPending ? '' : this.#state.pending, error: '',
        })
      }
    } catch (error) {
      if (generation === this.#generation) {
        this.#set({ ...this.#state, phase: 'error', error: error instanceof ControlPlaneError ? error.message : 'Model routes are unavailable' })
      }
      return
    }
    // The other target only feeds catalog badges, so it never delays this list.
    if (this.#tracksBothTargets) void this.#loadPublished(target === 'relay' ? 'tunnel' : 'relay', generation)
  }

  async #loadPublished(target: RouteTarget, generation: number): Promise<void> {
    try {
      const routes = await this.#port.list(target)
      if (generation === this.#generation) {
        this.#set({ ...this.#state, published: { ...this.#state.published, [target]: routes } })
      }
    } catch {
      // Badges stay as they were; the active target already reported its state.
    }
  }

  async upsert(route: ModelRoute): Promise<boolean> {
    return this.#mutate(route.publicModel, () => this.#port.upsert(route))
  }

  async upsertMany(routes: readonly ModelRoute[]): Promise<boolean> {
    if (routes.length === 0) return false
    return this.#mutate('*', () => this.#publishAll(routes))
  }

  /**
   * Makes the published routes of the active target match the catalog selection for
   * one provider: missing models are added, cleared models are removed.
   */
  async applySelection(providerId: string, selected: readonly string[]): Promise<boolean> {
    const target = this.#state.target
    const { additions, removals } = selectionChanges(this.#state.routes, target, providerId, selected)
    if (additions.length === 0 && removals.length === 0) return false
    if (removals.length > MAX_SELECTION_REMOVALS) {
      this.#set({ ...this.#state, error: `Clear at most ${MAX_SELECTION_REMOVALS} routes at once` })
      return false
    }
    return this.#mutate('*', async () => {
      if (additions.length > 0) await this.#publishAll(additions)
      for (const publicModel of removals) await this.#port.delete(target, publicModel)
    })
  }

  async delete(publicModel: string): Promise<boolean> {
    return this.#mutate(publicModel, () => this.#port.delete(this.#state.target, publicModel))
  }

  clearError(): void {
    if (this.#state.error) this.#set({ ...this.#state, error: '' })
  }

  dispose(): void {
    this.#unsubscribe?.()
    this.#listeners.clear()
  }

  async #publishAll(routes: readonly ModelRoute[]): Promise<void> {
    for (let offset = 0; offset < routes.length; offset += ROUTE_BATCH) {
      await this.#port.upsertMany(routes.slice(offset, offset + ROUTE_BATCH))
    }
  }

  async #mutate(pending: string, operation: () => Promise<unknown>): Promise<boolean> {
    if (this.#state.pending) return false
    this.#set({ ...this.#state, pending, error: '' })
    try {
      await operation()
      await this.load(this.#state.target, true)
      return true
    } catch (error) {
      await this.load(this.#state.target, true)
      this.#set({ ...this.#state, pending: '', error: error instanceof ControlPlaneError ? error.message : 'Route could not be saved' })
      return false
    }
  }

  #set(state: RoutesState): void {
    this.#state = state
    for (const listener of this.#listeners) listener()
  }
}
