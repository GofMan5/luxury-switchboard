export interface ModelTestResult {
  readonly providerId: string
  readonly model: string
  readonly state: 'testing' | 'available' | 'unavailable' | 'timeout'
  readonly status: number
  readonly latencyMs: number
  readonly errorCode?: string
}
