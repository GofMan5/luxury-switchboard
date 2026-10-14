import type { ControlPlaneSession } from '../../../platform/stdio/session'
import type { ModelsPort } from '../application/models-port'
import type { ModelTestProfile, ModelTestResult } from '../domain/model'

export class StdioModelsPort implements ModelsPort {
  readonly #session: ControlPlaneSession

  constructor(session: ControlPlaneSession) {
    this.#session = session
  }

  async discover(providerId: string, signal?: AbortSignal) {
    const result = await this.#session.call<{ models: readonly string[] }>('models.discover', { providerId }, signal)
    return result.models
  }

  async test(providerId: string, runId: string, models: readonly string[], signal?: AbortSignal) {
    const result = await this.#session.call<{ tested: number }>('models.test', { providerId, runId, models }, signal)
    return result.tested
  }

  async testProfile(providerId: string, runId: string, models: readonly string[], profile: ModelTestProfile, signal?: AbortSignal) {
    const result = await this.#session.call<{ tested: number }>('models.test', { providerId, runId, models, profile }, signal)
    return result.tested
  }

  subscribe(listener: (result: ModelTestResult) => void) {
    return this.#session.subscribe<ModelTestResult>('models.tested', (event) => {
      if (event.payload) listener(event.payload)
    })
  }
}
