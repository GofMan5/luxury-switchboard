import { describe, expect, it } from 'vitest'
import { publishedModels, selectionChanges, type ModelRoute, type RouteTarget } from '../domain/route'
import { RoutesModel } from './routes-model'
import type { RoutesPort } from './routes-port'
import { ControlPlaneError } from '../../../shared/contracts/protocol'

const route: ModelRoute = { target: 'relay', publicModel: 'public-model', upstreamModel: 'upstream-model', providerId: 'echo', contextLimitKiB: 0, enabled: true }

class Port implements RoutesPort {
  listener: ((target: RouteTarget) => void) | undefined
  finishUpsert: ((value: ModelRoute) => void) | undefined
  list: RoutesPort['list'] = async () => []
  upsert = async (_value: ModelRoute) => new Promise<ModelRoute>((resolve) => { this.finishUpsert = resolve; this.listener?.('relay') })
  batches: number[] = []
  upsertMany = async (routes: readonly ModelRoute[]) => { this.batches.push(routes.length); return routes.length }
  delete: RoutesPort['delete'] = async () => undefined
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

  it('does not expose actions for the previous target while switching', async () => {
    let finishSwitch!: (value: readonly ModelRoute[]) => void
    const port = new Port()
    port.list = async (target: RouteTarget) => target === 'relay' ? [route] : await new Promise<readonly ModelRoute[]>((resolve) => { finishSwitch = resolve })
    const model = new RoutesModel(port)
    await model.load('relay')
    const switching = model.load('tunnel')
    expect(model.snapshot()).toMatchObject({ target: 'tunnel', phase: 'loading', routes: [] })
    finishSwitch([])
    await switching
  })

  it('shows an actionable secure-storage failure', async () => {
    const port = new Port()
    port.upsert = async () => { throw new ControlPlaneError('secure_storage_unavailable', 'Unlock Linux Secret Service and restart Switchboard.') }
    const model = new RoutesModel(port)
    await model.load('relay')
    expect(await model.upsert(route)).toBe(false)
    expect(model.snapshot().error).toBe('Unlock Linux Secret Service and restart Switchboard.')
  })

  it('publishes missing models and clears removed ones in one apply step', async () => {
    const port = new Port()
    const published = [
      { ...route, publicModel: 'keep', upstreamModel: 'keep' },
      { ...route, publicModel: 'drop', upstreamModel: 'drop' },
      { ...route, publicModel: 'alias', upstreamModel: 'aliased-upstream' },
      { ...route, publicModel: 'other-provider', upstreamModel: 'other-provider', providerId: 'other' },
    ]
    port.list = async (target) => (target === 'relay' ? published : [])
    const deleted: string[] = []
    port.delete = async (_target: RouteTarget, publicModel: string) => { deleted.push(publicModel) }
    const created: ModelRoute[] = []
    port.upsertMany = async (routes: readonly ModelRoute[]) => { created.push(...routes); return routes.length }
    const model = new RoutesModel(port)
    await model.load('relay')

    expect(publishedModels(model.snapshot().routes, 'echo')).toEqual(['keep', 'drop', 'aliased-upstream'])
    expect(await model.applySelection('echo', ['keep', 'fresh'])).toBe(true)
    expect(created).toEqual([{ target: 'relay', publicModel: 'fresh', upstreamModel: 'fresh', providerId: 'echo', contextLimitKiB: 0, enabled: true }])
    expect(deleted).toEqual(['drop'])
  })

  it('never touches hand-made aliases or other providers', async () => {
    const port = new Port()
    port.list = async (target) => (target === 'relay' ? [
      { ...route, publicModel: 'alias', upstreamModel: 'aliased-upstream' },
      { ...route, publicModel: 'foreign', upstreamModel: 'foreign', providerId: 'other' },
    ] : [])
    const model = new RoutesModel(port)
    await model.load('relay')
    expect(selectionChanges(model.snapshot().routes, 'relay', 'echo', [])).toEqual({ additions: [], removals: [] })
    expect(await model.applySelection('echo', [])).toBe(false)
  })

  it('keeps the catalog badges of the other target without blocking the active list', async () => {
    const port = new Port()
    let finishTunnel!: (value: readonly ModelRoute[]) => void
    port.list = async (target) => target === 'relay'
      ? [route]
      : await new Promise<readonly ModelRoute[]>((resolve) => { finishTunnel = resolve })
    const model = new RoutesModel(port)
    await model.load('relay')
    expect(model.snapshot()).toMatchObject({ phase: 'ready', routes: [route] })
    finishTunnel([{ ...route, target: 'tunnel', publicModel: 'public-alias' }])
    await Promise.resolve()
    await Promise.resolve()
    expect(model.snapshot().published.tunnel).toEqual([{ ...route, target: 'tunnel', publicModel: 'public-alias' }])
  })
})
