import type { ControlPlaneSession } from '../../../platform/stdio/session'
import type { InsightsPeriod, InsightsReport, ModelPrice } from '../domain/insights'
import type { InsightsPort, PriceDraft } from '../application/insights-port'

export class StdioInsightsPort implements InsightsPort {
  readonly #session: ControlPlaneSession
  constructor(session: ControlPlaneSession) { this.#session = session }

  async report(period: InsightsPeriod, signal?: AbortSignal): Promise<InsightsReport> {
    return this.#session.call<InsightsReport>('analytics.report', { period }, signal)
  }

  async prices(signal?: AbortSignal): Promise<readonly ModelPrice[]> {
    const answer = await this.#session.call<{ prices: readonly ModelPrice[] }>('analytics.prices.get', {}, signal)
    return answer.prices ?? []
  }

  async setPrice(draft: PriceDraft, signal?: AbortSignal): Promise<readonly ModelPrice[]> {
    const answer = await this.#session.call<{ prices: readonly ModelPrice[] }>('analytics.prices.set', draft, signal)
    return answer.prices ?? []
  }

  async removePrice(model: string, signal?: AbortSignal): Promise<readonly ModelPrice[]> {
    const answer = await this.#session.call<{ prices: readonly ModelPrice[] }>('analytics.prices.remove', { model }, signal)
    return answer.prices ?? []
  }
}
