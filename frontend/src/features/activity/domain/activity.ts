export type ActivityState = 'active' | 'retrying' | 'completed' | 'failed' | 'cancelled'

export interface ActivityRequest {
  readonly id: string
  readonly startedAt: string
  readonly updatedAt: string
  readonly state: ActivityState
  readonly model: string
  readonly providerId: string
  readonly providerName: string
  readonly method: string
  readonly path: string
  readonly status?: number
  readonly queueMs: number
  readonly latencyMs: number
  readonly retries: number
  readonly bytesIn: number
  readonly bytesOut: number
  readonly errorCode?: string
  readonly errorDetail?: string
  readonly inputTokens: number
  readonly outputTokens: number
  readonly cachedTokens: number
  readonly reasoningTokens: number
  readonly totalTokens: number
  readonly contextTokens: number
  readonly generationMs: number
  readonly tokensPerSecond: number
}

export interface ActivitySummary {
  readonly requests: number
  readonly active: number
  readonly queued: number
  readonly successRate: number
  readonly p95Ms: number
  readonly rpm: number
}
