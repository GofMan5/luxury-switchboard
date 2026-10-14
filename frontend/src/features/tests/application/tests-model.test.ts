import { beforeEach, describe, expect, it, vi } from 'vitest'
import { TestsModel } from './tests-model'
import type { ModelTestResult } from '../../models/domain/model'
import type { ModelsPort } from '../../models/application/models-port'

class FakePort implements ModelsPort {
  catalogs = new Map<string, readonly string[]>()
  tested: { providerId: string; models: readonly string[] }[] = []
  testProfile?: ModelsPort['testProfile']
  listener?: (result: ModelTestResult) => void
  async discover(providerId: string) { return this.catalogs.get(providerId) ?? [] }
  async test(providerId: string, runId: string, models: readonly string[], _signal?: AbortSignal) {
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

  it('forwards an explicit full-context profile and keeps one run identity', async () => {
    const calls: { profile: string; models: readonly string[] }[] = []
    port.testProfile = async (providerId, runId, models, profile) => {
      calls.push({ profile, models })
      for (const modelName of models) port.listener?.({ runId, providerId, model: modelName, state: 'available', status: 200, latencyMs: 2_400, ttftMs: 1_500, outputTokens: 128 })
      return models.length
    }
    await model.run([{ providerId: 'p1', providerName: 'P1', models: ['m1', 'm2'] }], 'full-128k')
    expect(calls).toEqual([{ profile: 'full-128k', models: ['m1', 'm2'] }])
    expect(model.snapshot()).toMatchObject({ profile: 'full-128k', runDone: 2, runTotal: 2, running: false })
  })

  it('never silently replaces Full with a legacy short probe', async () => {
    await model.run([{ providerId: 'p1', providerName: 'P1', models: ['m1'] }], 'full-128k')
    expect(port.tested).toEqual([])
    expect(model.snapshot()).toMatchObject({ running: false, profile: 'full-128k', runDone: 0 })
    expect(model.snapshot().error).toContain('not supported')
  })

  it('cancels a full-context request and ignores its late result', async () => {
    let activeRun = ''
    let activeSignal: AbortSignal | undefined
    port.testProfile = (_providerId, runId, _models, _profile, signal) => {
      activeRun = runId
      activeSignal = signal
      return new Promise<number>((_resolve, reject) => signal?.addEventListener('abort', () => reject(new Error('cancelled')), { once: true }))
    }
    const running = model.run([{ providerId: 'p1', providerName: 'P1', models: ['m1', 'm2'] }], 'full-128k')
    expect(model.snapshot().running).toBe(true)
    model.cancel()
    await running
    expect(activeSignal?.aborted).toBe(true)
    expect(model.snapshot()).toMatchObject({ running: false, profile: 'full-128k', runDone: 2, runFailed: 2 })
    port.listener?.({ runId: activeRun, providerId: 'p1', model: 'm1', state: 'available', status: 200, latencyMs: 1 })
    expect(model.snapshot().results['p1 m1']?.errorCode).toBe('interrupted')
  })

  it.each([500, 501, 1_201])('measures %i models in bounded batches with one run identity and exact progress', async (count) => {
    const measure = port.test.bind(port)
    const identities: string[] = []
    port.test = async (providerId, runId, models, signal) => {
      if (models.length > 500) throw new Error('too many models')
      identities.push(runId)
      expect(signal?.aborted).toBe(false)
      return measure(providerId, runId, models, signal)
    }
    const models = Array.from({ length: count }, (_, index) => `m-${index}`)
    await model.run([{ providerId: 'p1', providerName: 'P1', models }])
    expect(port.tested.map((call) => call.models.length)).toEqual(count === 500 ? [500] : count === 501 ? [500, 1] : [500, 500, 201])
    expect(new Set(identities).size).toBe(1)
    expect(model.snapshot()).toMatchObject({ runDone: count, runTotal: count, runFailed: 0, running: false, error: '' })
    expect(Object.values(model.snapshot().results)).toHaveLength(count)
    expect(Object.values(model.snapshot().results).every((result) => result.state === 'available')).toBe(true)
    model.dispose()
  })

  it('cancels between batches, settles unstarted rows, and ignores late events', async () => {
    const measure = port.test.bind(port)
    let finish!: () => void
    let interruptedRun = ''
    port.test = async (providerId, runId, models, signal) => {
      interruptedRun = runId
      await measure(providerId, runId, models, signal)
      return new Promise<number>((resolve) => { finish = () => resolve(models.length) })
    }
    const models = Array.from({ length: 501 }, (_, index) => `m-${index}`)
    const running = model.run([
      { providerId: 'p1', providerName: 'P1', models },
      { providerId: 'p2', providerName: 'P2', models: ['m3'] },
    ])
    await Promise.resolve()
    model.cancel()
    finish()
    await running
    expect(port.tested.map((call) => call.models.length)).toEqual([500])
    expect(model.snapshot()).toMatchObject({ runDone: 502, runTotal: 502, runFailed: 2, running: false, error: '' })
    expect(model.snapshot().results['p1 m-500']?.errorCode).toBe('interrupted')
    expect(model.snapshot().results['p2 m3']?.errorCode).toBe('interrupted')
    port.listener?.({ runId: interruptedRun, providerId: 'p1', model: 'm-500', state: 'available', status: 200, latencyMs: 1 })
    expect(model.snapshot().results['p1 m-500']?.errorCode).toBe('interrupted')
    model.dispose()
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
