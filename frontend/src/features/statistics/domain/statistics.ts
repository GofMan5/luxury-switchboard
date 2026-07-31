import type { ActivityRequest } from '../../activity/domain/activity'

export type StatisticsPeriod = '24h' | '48h' | '72h' | 'all'

export interface HistoryStats {
  readonly requests: number
  readonly completed: number
  readonly failed: number
  readonly cancelled: number
  readonly retries: number
  readonly inputTokens: number
  readonly outputTokens: number
  readonly cachedTokens: number
  readonly reasoningTokens: number
  readonly processedTokens: number
  readonly nonCachedTokens: number
  readonly p95Ms: number
  readonly tokensPerSecond: number
}

export interface StatisticsSnapshot {
  readonly stats: HistoryStats
  readonly recent: readonly ActivityRequest[]
}
