export type RouteTarget = 'relay' | 'tunnel'
export interface ModelRoute { readonly target: RouteTarget; readonly publicModel: string; readonly upstreamModel: string; readonly providerId: string; readonly contextLimitKiB: number; readonly enabled: boolean }
