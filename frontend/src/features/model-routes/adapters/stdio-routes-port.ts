import type { ControlPlaneSession } from '../../../platform/stdio/session'
import type { RoutesPort } from '../application/routes-port'
import type { ModelRoute, RouteTarget } from '../domain/route'

export class StdioRoutesPort implements RoutesPort {
  readonly #session: ControlPlaneSession

  constructor(session: ControlPlaneSession) {
    this.#session = session
  }

  async list(target: RouteTarget, signal?: AbortSignal): Promise<readonly ModelRoute[]> {
    const result = await this.#session.call<{ routes: readonly ModelRoute[] }>(
      'routes.list',
      { target },
      signal,
    )
    return result.routes
  }

  upsert(route: ModelRoute, signal?: AbortSignal): Promise<ModelRoute> {
    return this.#session.call<ModelRoute>('routes.upsert', route, signal)
  }

  async upsertMany(routes: readonly ModelRoute[], signal?: AbortSignal): Promise<number> {
    const result = await this.#session.call<{ saved: number }>('routes.upsertMany', { routes }, signal)
    return result.saved
  }

  async delete(target: RouteTarget, publicModel: string, signal?: AbortSignal): Promise<void> {
    await this.#session.call('routes.delete', { target, publicModel }, signal)
  }

  subscribe(listener: (target: RouteTarget) => void): () => void {
    return this.#session.subscribe<{ target: RouteTarget }>('routes.changed', (event) => {
      if (event.payload?.target) listener(event.payload.target)
    })
  }
}
