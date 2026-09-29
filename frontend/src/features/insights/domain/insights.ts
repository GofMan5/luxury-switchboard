export type InsightsPeriod = '24h' | '48h' | '72h' | 'all'

export interface InsightsVolume {
  readonly requests: number
  readonly completed: number
  readonly failed: number
  readonly cancelled: number
  readonly retries: number
  readonly inputTokens: number
  readonly outputTokens: number
  readonly cachedTokens: number
  readonly reasoningTokens: number
  readonly totalTokens: number
  readonly generationMs: number
  /** Estimate built from the operator's price catalog; zero and isPriced=false when the model has no price. */
  readonly cost: number
  readonly isPriced: boolean
}

export interface InsightsProvider {
  readonly id: string
  readonly name: string
  readonly volume: InsightsVolume
  readonly p50Ms: number
  readonly p95Ms: number
  readonly avgMs: number
  readonly tokensPerSecond: number
  readonly errors: readonly InsightsErrorCount[]
}

export interface InsightsModel {
  readonly model: string
  readonly providerName: string
  readonly volume: InsightsVolume
  readonly p50Ms: number
  readonly p95Ms: number
  readonly avgMs: number
  readonly tokensPerSecond: number
  readonly errors: readonly InsightsErrorCount[]
}

export interface InsightsDailyPoint {
  readonly date: string
  readonly volume: InsightsVolume
  readonly successRate: number
  readonly tokensPerSecond: number
}

export interface InsightsErrorCount {
  readonly errorCode: string
  readonly requests: number
}

export interface InsightsOverview {
  readonly volume: InsightsVolume
  readonly successRate: number
  readonly p50Ms: number
  readonly p95Ms: number
  readonly tokensPerSecond: number
  readonly pricedRequests: number
  readonly topErrorCode: string
}

export interface InsightsReport {
  readonly period: InsightsPeriod
  readonly generatedAt: string
  readonly overview: InsightsOverview
  readonly providers: readonly InsightsProvider[]
  readonly models: readonly InsightsModel[]
  readonly daily: readonly InsightsDailyPoint[]
  readonly errors: readonly InsightsErrorCount[]
  readonly unpricedModels: readonly string[]
}

export interface ModelPrice {
  readonly model: string
  readonly input: number
  readonly cachedInput: number
  readonly output: number
  readonly reasoning: number
  readonly updatedAt: string
}

/** One persisted request row, as the history slice sanitized it. */
export interface HistoryRequest {
  readonly id: string
  readonly state: 'active' | 'retrying' | 'completed' | 'failed' | 'cancelled'
  readonly model: string
  readonly providerId: string
  readonly status: number
  readonly latencyMs: number
  readonly totalTokens: number
  readonly cachedTokens: number
  readonly updatedAt: string
  readonly errorCode: string
  readonly errorDetail: string
}
