import { describe, expect, it } from 'vitest'
import type { ModelRoute, RouteTarget } from '../domain/route'
import { RoutesModel } from './routes-model'
import type { RoutesPort } from './routes-port'

const route: ModelRoute = { target: 'relay', publicModel: 'public-model', upstreamModel: 'upstream-model', providerId: 'echo', contextLimitKiB: 0, enabled: true }

class Port implements RoutesPort {
  listener: ((target: RouteTarget) => void) | undefined
  finishUpsert: ((value: ModelRoute) => void) | undefined
  list = async () => []
  upsert = async (_value: ModelRoute) => new Promise<ModelRoute>((resolve) => { this.finishUpsert = resolve; this.listener?.('relay') })
  batches: number[] = []
  upsertMany = async (routes: readonly ModelRoute[]) => { this.batches.push(routes.length); return routes.length }
  delete = async () => undefined
  subscribe = (listener: (target: RouteTarget) => void) => { this.listener = listener; return () => undefined }
}

describe('RoutesModel', () => {
  it('keeps a mutation pending while its change event refreshes the list', async () => {
    const port = new Port()
    const model = new RoutesModel(port)
    await model.load('relay')
    const mutation = model.upsert(route)
    await Promise.resolve()
    await Promise.resolve()
    expect(model.snapshot().pending).toBe('public-model')
    port.finishUpsert?.(route)
    expect(await mutation).toBe(true)
    expect(model.snapshot().pending).toBe('')
  })

  it('batches large provider catalogs within the control-plane limit', async () => {
    const port = new Port()
    const model = new RoutesModel(port)
    await model.load('relay')
    const routes = Array.from({ length: 1_201 }, (_, index) => ({ ...route, publicModel: `public-${index}`, upstreamModel: `upstream-${index}` }))
    expect(await model.upsertMany(routes)).toBe(true)
    expect(port.batches).toEqual([500, 500, 201])
  })
})
