import type { ModelTestProfile, ModelTestResult } from '../domain/model'

export interface ModelsPort {
  discover(providerId: string, signal?: AbortSignal): Promise<readonly string[]>
  test(providerId: string, runId: string, models: readonly string[], signal?: AbortSignal): Promise<number>
  /** Optional capability for sidecars that understand explicit test budgets. */
  testProfile?(providerId: string, runId: string, models: readonly string[], profile: ModelTestProfile, signal?: AbortSignal): Promise<number>
  subscribe(listener: (result: ModelTestResult) => void): () => void
}
