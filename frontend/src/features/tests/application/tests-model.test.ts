import { beforeEach, describe, expect, it, vi } from 'vitest'
import { TestsModel } from './tests-model'
import type { ModelTestResult } from '../../models/domain/model'
import type { ModelsPort } from '../../models/application/models-port'

class FakePort implements ModelsPort {
  catalogs = new Map<string, readonly string[]>()
  tested: { providerId: string; models: readonly string[] }[] = []
  listener: ((result: ModelTestResult) => void) | undefined
  async discover(providerId: string) { return this.catalogs.get(providerId) ?? [] }
  async test(providerId: string, runId: string, models: readonly string[]) {
    this.tested.push({ providerId, models })
    for (const model of models) {
      this.listener?.({ runId, providerId, model, state: 'available', status: 200, latencyMs: 1_200, ttftMs: 300, outputTokens: 32 })
    }
    return models.length
  }
  subscribe(listener: (result: ModelTestResult) => void) { this.listener = listener; return () => { this.listener = undefined } }
}

describe('TestsModel', () => {
  let port: FakePort
  let model: TestsModel
  beforeEach(() => {
    port = new FakePort()
    port.catalogs.set('p1', ['m1', 'm2'])
    port.catalogs.set('p2', ['m3'])
    model = new TestsModel(port)
    model.connect()
  })

  it('runs the providers in sequence and keeps their rows separate', async () => {
    await model.run([
      { providerId: 'p1', providerName: 'P1', models: ['m1', 'm2'] },
      { providerId: 'p2', providerName: 'P2', models: ['m3'] },
    ])
    expect(port.tested.map((call) => call.providerId)).toEqual(['p1', 'p2'])
    const results = model.snapshot().results
    expect(results['p1 m1']?.ttftMs).toBe(300)
    expect(results['p2 m3']?.state).toBe('available')
    expect(model.snapshot().running).toBe(false)
  })

  it('marks a model that never reported instead of leaving a spinner', async () => {
    port.test = async (providerId, runId, models) => {
      // Only the first model reports; the second must not spin forever.
      const model = models[0]
      if (model) port.listener?.({ runId, providerId, model, state: 'available', status: 200, latencyMs: 900, ttftMs: 200, outputTokens: 16 })
      return models.length
    }
    await model.run([{ providerId: 'p1', providerName: 'P1', models: ['m1', 'm2'] }])
    expect(model.snapshot().results['p1 m1']?.state).toBe('available')
    expect(model.snapshot().results['p1 m2']?.errorCode).toBe('result_missing')
  })

  it('ignores events of another run', async () => {
    await model.run([{ providerId: 'p1', providerName: 'P1', models: ['m1'] }])
    port.listener?.({ runId: 'probe_stale', providerId: 'p1', model: 'm1', state: 'unavailable', status: 500, latencyMs: 10 })
    expect(model.snapshot().results['p1 m1']?.state).toBe('available')
  })

  it('caches a discovered catalog and re-reads it only when asked', async () => {
    const discover = vi.spyOn(port, 'discover')
    await model.ensureCatalog('p1')
    await model.ensureCatalog('p1')
    expect(discover).toHaveBeenCalledTimes(1)
    await model.refreshCatalog('p1')
    expect(discover).toHaveBeenCalledTimes(2)
  })

  it('a catalog failure reads as its own state, not as an empty catalog', async () => {
    port.discover = async () => { throw new Error('refused') }
    await model.ensureCatalog('p1')
    expect(model.snapshot().catalogs['p1']).toBe('error')
  })
})
