export interface ModelTestResult {
  readonly runId: string
  readonly providerId: string
  readonly model: string
  readonly state: 'testing' | 'available' | 'unavailable' | 'timeout'
  readonly status: number
  readonly latencyMs: number
  /** Time to the first content token of the streaming probe; zero on a
   * refusal. OutputTokens is the provider's own count. */
  readonly ttftMs?: number
  readonly outputTokens?: number
  readonly errorCode?: string
}
