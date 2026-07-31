import { describe, expect, it } from 'vitest'
import type { ModelTestResult } from '../domain/model'
import type { ModelsPort } from './models-port'
import { ModelsModel } from './models-model'

class FakeModelsPort implements ModelsPort {
  async discover() { return ['model-a', 'model-b'] }
  async test(_providerId: string, models: readonly string[]) { return models.length }
  subscribe(_listener: (result: ModelTestResult) => void) { return () => undefined }
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
})
