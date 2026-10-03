import type { HistoryRequest, InsightsPeriod, InsightsReport, PriceCatalog } from '../domain/insights'

export interface PriceDraft {
  readonly model: string
  readonly input: number
  readonly cachedInput: number
  readonly output: number
  readonly reasoning: number
}

/** The recent persisted rows and how many of them the answer carries. The
 * backend bounds the list to one protocol frame, so a short list is the newest
 * part of a longer journal rather than all of it — `available` is the honest
 * total the section header can name. */
export interface RecentRequests {
  readonly rows: readonly HistoryRequest[]
  readonly available: number
}

export interface InsightsPort {
  report(period: InsightsPeriod, signal?: AbortSignal): Promise<InsightsReport>
  recent(period: InsightsPeriod, signal?: AbortSignal): Promise<RecentRequests>
  prices(signal?: AbortSignal): Promise<PriceCatalog>
  setPrice(draft: PriceDraft, signal?: AbortSignal): Promise<PriceCatalog>
  removePrice(model: string, signal?: AbortSignal): Promise<PriceCatalog>
  setCurrency(currency: string, signal?: AbortSignal): Promise<PriceCatalog>
}
