import { describe, expect, it } from 'vitest'
import type { ModelTestResult } from '../domain/model'
import type { ModelsPort } from './models-port'
import { ModelsModel } from './models-model'

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
  async test(_providerId: string, models: readonly string[], signal?: AbortSignal) {
    this.testBatches.push(models.length)
    if (this.fail) throw new Error('injected')
    if (this.waitForAbort) await new Promise<void>((_resolve, reject) => signal?.addEventListener('abort', () => reject(new Error('cancelled')), { once: true }))
    for (const model of models) this.listener?.({ providerId: _providerId, model, state: 'available', status: 200, latencyMs: 1 })
    return models.length
  }
  subscribe(listener: (result: ModelTestResult) => void) { this.listener = listener; return () => { this.listener = undefined } }
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
})
