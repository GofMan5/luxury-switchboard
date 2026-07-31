import type { ModelTestResult } from '../domain/model'

export interface ModelsPort {
  discover(providerId: string, signal?: AbortSignal): Promise<readonly string[]>
  test(providerId: string, models: readonly string[], signal?: AbortSignal): Promise<number>
  subscribe(listener: (result: ModelTestResult) => void): () => void
}
