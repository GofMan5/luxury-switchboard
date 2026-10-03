import type { ControlPlaneSession } from '../../../platform/stdio/session'
import type { HistoryRequest, InsightsPeriod, InsightsReport, PriceCatalog } from '../domain/insights'
import type { InsightsPort, PriceDraft, RecentRequests } from '../application/insights-port'

export class StdioInsightsPort implements InsightsPort {
  readonly #session: ControlPlaneSession
  constructor(session: ControlPlaneSession) { this.#session = session }

  async report(period: InsightsPeriod, signal?: AbortSignal): Promise<InsightsReport> {
    return this.#session.call<InsightsReport>('analytics.report', { period }, signal)
  }

  async recent(period: InsightsPeriod, signal?: AbortSignal): Promise<RecentRequests> {
    const answer = await this.#session.call<{ requests?: readonly HistoryRequest[]; available?: number }>('history.recent', { period, limit: 100 }, signal)
    const rows = answer.requests ?? []
    return { rows, available: answer.available ?? rows.length }
  }

  async prices(signal?: AbortSignal): Promise<PriceCatalog> {
    const answer = await this.#session.call<{ prices?: PriceCatalog['prices']; currency?: string }>('analytics.prices.get', {}, signal)
    return { prices: answer.prices ?? [], currency: answer.currency ?? 'USD' }
  }

  async setPrice(draft: PriceDraft, signal?: AbortSignal): Promise<PriceCatalog> {
    const answer = await this.#session.call<{ prices?: PriceCatalog['prices']; currency?: string }>('analytics.prices.set', draft, signal)
    return { prices: answer.prices ?? [], currency: answer.currency ?? 'USD' }
  }

  async removePrice(model: string, signal?: AbortSignal): Promise<PriceCatalog> {
    const answer = await this.#session.call<{ prices?: PriceCatalog['prices']; currency?: string }>('analytics.prices.remove', { model }, signal)
    return { prices: answer.prices ?? [], currency: answer.currency ?? 'USD' }
  }

  async setCurrency(currency: string, signal?: AbortSignal): Promise<PriceCatalog> {
    const answer = await this.#session.call<{ prices?: PriceCatalog['prices']; currency?: string }>('analytics.prices.setCurrency', { currency }, signal)
    return { prices: answer.prices ?? [], currency: answer.currency ?? 'USD' }
  }
}
