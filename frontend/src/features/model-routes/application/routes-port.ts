import type { ModelRoute, RouteTarget } from '../domain/route'

export interface RoutesPort {
  list(target: RouteTarget, signal?: AbortSignal): Promise<readonly ModelRoute[]>
  upsert(route: ModelRoute, signal?: AbortSignal): Promise<ModelRoute>
  upsertMany(routes: readonly ModelRoute[], signal?: AbortSignal): Promise<number>
  delete(target: RouteTarget, publicModel: string, signal?: AbortSignal): Promise<void>
  subscribe(listener: (target: RouteTarget) => void): () => void
}
