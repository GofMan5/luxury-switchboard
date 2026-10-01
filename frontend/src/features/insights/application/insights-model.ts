import type { HistoryRequest, InsightsPeriod, InsightsReport, ModelPrice } from '../domain/insights'
import type { InsightsPort, PriceDraft } from './insights-port'

export interface InsightsState {
  readonly phase: 'idle' | 'loading' | 'ready' | 'error'
  readonly period: InsightsPeriod
  readonly report: InsightsReport | null
  /** The persisted request rows behind the numbers: the same period, the same honesty. */
  readonly recent: readonly HistoryRequest[]
  /** How many rows the period actually holds; the list is bounded to one frame. */
  readonly recentAvailable: number
  readonly prices: readonly ModelPrice[]
  readonly pricesPhase: 'idle' | 'loading' | 'saving' | 'ready' | 'error'
  readonly error: string
}

/** Reload the report after a price edit: the cost columns are the point. */
export class InsightsModel {
  readonly #port: InsightsPort
  #state: InsightsState = { phase: 'idle', period: '24h', report: null, recent: [], recentAvailable: 0, prices: [], pricesPhase: 'idle', error: '' }
  #listeners = new Set<() => void>()
  #generation = 0
  constructor(port: InsightsPort) { this.#port = port }
  snapshot = (): InsightsState => this.#state
  subscribe = (listener: () => void): (() => void) => { this.#listeners.add(listener); return () => this.#listeners.delete(listener) }

  async load(period: InsightsPeriod): Promise<void> {
    const generation = ++this.#generation
    this.#set({ ...this.#state, phase: 'loading', period, report: period === this.#state.period ? this.#state.report : null, recent: period === this.#state.period ? this.#state.recent : [], recentAvailable: period === this.#state.period ? this.#state.recentAvailable : 0, error: '' })
    try {
      const [report, recent, prices] = await Promise.all([
        this.#port.report(period),
        this.#port.recent(period),
        this.#state.pricesPhase === 'idle' ? this.#port.prices() : Promise.resolve(this.#state.prices),
      ])
      if (generation === this.#generation) this.#set({ phase: 'ready', period, report, recent: recent.rows, recentAvailable: recent.available, prices, pricesPhase: 'ready', error: '' })
    } catch {
      if (generation === this.#generation) this.#set({ ...this.#state, phase: 'error', error: 'Insights are unavailable' })
    }
  }

  /** True when the price reached the catalog; false leaves the editor open. */
  async savePrice(draft: PriceDraft): Promise<boolean> {
    const generation = this.#generation
    this.#set({ ...this.#state, pricesPhase: 'saving', error: '' })
    try {
      const prices = await this.#port.setPrice(draft)
      if (generation === this.#generation) this.#set({ ...this.#state, prices, pricesPhase: 'ready' })
      // The estimate only changes once the backend re-reads the catalog, so
      // the report is refreshed, not patched client-side.
      await this.load(this.#state.period)
      return true
    } catch {
      if (generation === this.#generation) this.#set({ ...this.#state, pricesPhase: 'error', error: 'The price was not saved' })
      return false
    }
  }

  /** True when the model was dropped; false leaves the editor open. */
  async removePrice(model: string): Promise<boolean> {
    const generation = this.#generation
    this.#set({ ...this.#state, pricesPhase: 'saving', error: '' })
    try {
      const prices = await this.#port.removePrice(model)
      if (generation === this.#generation) this.#set({ ...this.#state, prices, pricesPhase: 'ready' })
      await this.load(this.#state.period)
      return true
    } catch {
      if (generation === this.#generation) this.#set({ ...this.#state, pricesPhase: 'error', error: 'The price was not removed' })
      return false
    }
  }

  dispose(): void { this.#listeners.clear() }
  #set(state: InsightsState): void { this.#state = state; for (const listener of this.#listeners) listener() }
}
