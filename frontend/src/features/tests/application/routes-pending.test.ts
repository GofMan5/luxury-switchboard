import { describe, expect, it, vi } from 'vitest'
import { RoutesModel } from '../../model-routes/application/routes-model'
import type { RoutesPort } from '../../model-routes/application/routes-port'
import type { ModelRoute } from '../../model-routes/domain/route'

const route: ModelRoute = { target: 'relay', publicModel: 'public', upstreamModel: 'upstream', providerId: 'fixture', contextLimitKiB: 0, aliases: [], enabled: true }

describe('route mutation reconciliation', () => {
  it('releases pending after a saved route cannot refresh, allowing a later save', async () => {
    const list = vi.fn<RoutesPort['list']>().mockResolvedValue([route])
    const upsert = vi.fn<RoutesPort['upsert']>().mockResolvedValue(route)
    const port: RoutesPort = { list, upsert, upsertMany: async () => 0, remove: async () => {}, delete: async () => {}, subscribe: () => () => {} }
    const model = new RoutesModel(port, { tracksBothTargets: false })
    await model.load('relay')
    list.mockRejectedValueOnce(new Error('offline'))

    expect(await model.upsert(route)).toBe(true)
    expect(model.snapshot()).toMatchObject({ pending: '', phase: 'error', error: 'Model routes are unavailable' })
    await model.load('relay')
    expect(await model.upsert(route)).toBe(true)
    expect(upsert).toHaveBeenCalledTimes(2)
    expect(model.snapshot()).toMatchObject({ pending: '', phase: 'ready' })
    model.dispose()
  })
})
