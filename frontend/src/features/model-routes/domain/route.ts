export type RouteTarget = 'relay' | 'tunnel'
export interface ModelRoute {
  readonly target: RouteTarget
  readonly publicModel: string
  readonly upstreamModel: string
  readonly providerId: string
  readonly contextLimitKiB: number
  /** Older profiles and routes without aliases omit the field on the wire. */
  readonly aliases?: readonly string[]
  readonly enabled: boolean
  /** Relay chains: the order a request tries providers of the same public
   * model in, lower first. Tunnel routes keep one entry per model and ignore
   * it. Older control planes omit the field; it reads as zero. */
  readonly priority?: number
}

/** Models of one provider that the given routes already publish. */
export function publishedModels(routes: readonly ModelRoute[], providerId: string): readonly string[] {
  return routes.filter((route) => route.providerId === providerId).map((route) => route.upstreamModel)
}

export interface SelectionChanges {
  readonly additions: readonly ModelRoute[]
  readonly removals: readonly string[]
}

/**
 * Difference between a catalog selection and the published routes of one provider.
 * Hand-made aliases and routes of other providers are never part of it, so applying
 * a selection can only add or remove the plain one-to-one routes it owns.
 */
export function selectionChanges(routes: readonly ModelRoute[], target: RouteTarget, providerId: string, selected: readonly string[]): SelectionChanges {
  const selection = new Set(selected)
  const aliases = new Set(routes.map((route) => route.publicModel))
  const routed = new Set(publishedModels(routes, providerId))
  const additions = selected
    .filter((model) => !routed.has(model) && !aliases.has(model))
    .map((model) => ({ target, publicModel: model, upstreamModel: model, providerId, contextLimitKiB: 0, aliases: [], enabled: true } satisfies ModelRoute))
  const removals = routes
    .filter((route) => route.providerId === providerId && route.publicModel === route.upstreamModel && !selection.has(route.upstreamModel))
    .map((route) => route.publicModel)
  return { additions, removals }
}
