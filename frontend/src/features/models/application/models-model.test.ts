import { describe, expect, it, vi } from 'vitest'
import type { ModelTestResult } from '../domain/model'
import type { ModelsPort } from './models-port'
import { ModelsModel } from './models-model'
import { ControlPlaneError } from '../../../shared/contracts/protocol'

class FakeModelsPort implements ModelsPort {
  testBatches: number[] = []
  fail = false
  waitForAbort = false
  waitingDiscovery = ''
  discoveryAborted = false
  listener: ((result: ModelTestResult) => void) | undefined
  async discover(providerId: string, signal?: AbortSignal) {
    if (providerId === this.waitingDiscovery) {
      await new Promise<void>((_resolve, reject) => signal?.addEventListener('abort', () => { this.discoveryAborted = true; reject(new Error('cancelled')) }, { once: true }))
    }
    return ['model-a', 'model-b']
  }
  async test(_providerId: string, runId: string, models: readonly string[], signal?: AbortSignal) {
    this.testBatches.push(models.length)
    if (this.fail) throw new Error('injected')
    if (this.waitForAbort) await new Promise<void>((_resolve, reject) => signal?.addEventListener('abort', () => reject(new Error('cancelled')), { once: true }))
    for (const model of models) this.listener?.({ runId, providerId: _providerId, model, state: 'available', status: 200, latencyMs: 1 })
    return models.length
  }
  subscribe(listener: (result: ModelTestResult) => void) { this.listener = listener; return () => { this.listener = undefined } }
}

class DeferredModelsPort extends FakeModelsPort {
  resolvers: Array<(count: number) => void> = []
  override async test(_providerId: string, _runId: string, models: readonly string[]) {
    this.testBatches.push(models.length)
    return await new Promise<number>((resolve) => this.resolvers.push(resolve))
  }
}

describe('ModelsModel', () => {
  it('uses the second All models click to clear every selection', async () => {
    const model = new ModelsModel(new FakeModelsPort())
    model.connect()
    await model.discover('provider-a')
    model.toggleAll()
    expect(model.snapshot().selected).toEqual(['model-a', 'model-b'])
    model.toggleAll()
    expect(model.snapshot().selected).toEqual([])
    model.dispose()
  })

  it('re-discovery clears the selection, so re-seeding restores it', async () => {
    // The Model Routes effect mirrors the published routes into the checkboxes
    // after every completed discovery. Its reset only works because a
    // re-discovery of the very same catalog clears the selection: without
    // that, a plain Refresh left the checkboxes empty and armed "Apply to
    // relay" to delete the routes the seed was supposed to mirror.
    const model = new ModelsModel(new FakeModelsPort())
    model.connect()
    await model.discover('provider-a')
    model.select(['model-a', 'model-b'])
    expect(model.snapshot().selected).toEqual(['model-a', 'model-b'])
    await model.discover('provider-a')
    expect(model.snapshot().selected).toEqual([])
    model.select(['model-a'])
    expect(model.snapshot().selected).toEqual(['model-a'])
    model.dispose()
  })

  it('chunks large tests and clears testing rows after interruption', async () => {
    const port = new FakeModelsPort()
    const model = new ModelsModel(port)
    let notifications = 0
    model.subscribe(() => { notifications++ })
    model.connect()
    await model.discover('provider-a')
    const models = Array.from({ length: 501 }, (_, index) => `model-${index}`)
    expect(await model.test(models)).toBe(true)
    expect(port.testBatches).toEqual([500, 1])
    expect(notifications).toBeLessThan(20)
    port.fail = true
    expect(await model.test(['model-a', 'model-b'])).toBe(false)
    expect(Object.values(model.snapshot().results).some((result) => result.state === 'testing')).toBe(false)
    model.dispose()
  })

  it('cancels the previous provider test before discovery switches', async () => {
    const port = new FakeModelsPort()
    const model = new ModelsModel(port)
    model.connect()
    await model.discover('provider-a')
    port.waitForAbort = true
    const testing = model.test(['model-a'])
    await Promise.resolve()
    await model.discover('provider-b')
    expect(await testing).toBe(false)
    expect(model.snapshot()).toMatchObject({ providerId: 'provider-b', testing: false, error: '' })
    model.dispose()
  })

  it('terminates a stalled model test run at the safety limit', async () => {
    vi.useFakeTimers()
    const port = new FakeModelsPort()
    port.waitForAbort = true
    const model = new ModelsModel(port)
    model.connect()
    await model.discover('provider-a')
    const testing = model.test(['model-a'])
    await Promise.resolve()
    await vi.advanceTimersByTimeAsync(2 * 60_000)
    expect(await testing).toBe(false)
    expect(model.snapshot()).toMatchObject({ testing: false, error: 'Model tests reached the 2 minute safety limit' })
    expect(model.snapshot().results['model-a']).toMatchObject({ state: 'unavailable', errorCode: 'timeout' })
    model.dispose()
    vi.useRealTimers()
  })

  it('cancels stale discovery before loading another provider', async () => {
    const port = new FakeModelsPort()
    port.waitingDiscovery = 'provider-a'
    const model = new ModelsModel(port)
    const stale = model.discover('provider-a')
    await Promise.resolve()
    await model.discover('provider-b')
    await stale
    expect(port.discoveryAborted).toBe(true)
    expect(model.snapshot()).toMatchObject({ providerId: 'provider-b', phase: 'ready' })
    model.dispose()
  })

  it('does not let an old test completion clear a newer test', async () => {
    const port = new DeferredModelsPort()
    const model = new ModelsModel(port)
    model.connect()
    await model.discover('provider-a')
    const stale = model.test(['model-a'])
    await Promise.resolve()
    await model.discover('provider-a')
    const current = model.test(['model-b'])
    await Promise.resolve()
    port.resolvers[0](1)
    expect(await stale).toBe(false)
    expect(model.snapshot().testing).toBe(true)
    port.resolvers[1](1)
    expect(await current).toBe(true)
    expect(model.snapshot().testing).toBe(false)
    model.dispose()
  })

  it('stores hostile model names without changing the results prototype', async () => {
    const model = new ModelsModel(new FakeModelsPort())
    model.connect()
    await model.discover('provider-a')
    expect(await model.test(['__proto__'])).toBe(true)
    const results = model.snapshot().results
    expect(Object.getPrototypeOf(results)).toBeNull()
    expect(results.__proto__).toMatchObject({ model: '__proto__', state: 'available' })
    model.dispose()
  })

  it('shows why discovery failed instead of one standing guess about the key', async () => {
    const port = new FakeModelsPort()
    port.discover = async () => { throw new ControlPlaneError('model_discovery_unauthorized', 'Provider refused the stored key. Replace it in API Keys.') }
    const model = new ModelsModel(port)
    await model.discover('echo')
    expect(model.snapshot().error).toBe('Provider refused the stored key. Replace it in API Keys.')

    port.discover = async () => { throw new Error('socket closed') }
    await model.discover('echo')
    expect(model.snapshot().error).toContain('Check the provider address')
    model.dispose()
  })
})
