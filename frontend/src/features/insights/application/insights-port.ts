import type { HistoryRequest, InsightsPeriod, InsightsReport, ModelPrice } from '../domain/insights'

export interface PriceDraft {
  readonly model: string
  readonly input: number
  readonly cachedInput: number
  readonly output: number
  readonly reasoning: number
}

export interface InsightsPort {
  report(period: InsightsPeriod, signal?: AbortSignal): Promise<InsightsReport>
  recent(period: InsightsPeriod, signal?: AbortSignal): Promise<readonly HistoryRequest[]>
  prices(signal?: AbortSignal): Promise<readonly ModelPrice[]>
  setPrice(draft: PriceDraft, signal?: AbortSignal): Promise<readonly ModelPrice[]>
  removePrice(model: string, signal?: AbortSignal): Promise<readonly ModelPrice[]>
}
