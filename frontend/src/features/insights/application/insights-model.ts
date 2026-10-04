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
  readonly pricesCurrency: string
  readonly pricesPhase: 'idle' | 'loading' | 'saving' | 'ready' | 'error'
  readonly error: string
}

/** Reload the report after a price edit: the cost columns are the point. */
type PeriodAnswer = {
  readonly report: InsightsReport
  readonly recent: readonly HistoryRequest[]
  readonly recentAvailable: number
}

export class InsightsModel {
  readonly #port: InsightsPort
  #state: InsightsState = { phase: 'idle', period: '24h', report: null, recent: [], recentAvailable: 0, prices: [], pricesCurrency: 'USD', pricesPhase: 'idle', error: '' }
  /** Every answered period stays: switching back is instant, and the quiet
   * refresh rewrites the answer underneath. */
  readonly #cache = new Map<InsightsPeriod, PeriodAnswer>()
  #listeners = new Set<() => void>()
  #generation = 0
  #refreshing = false
  constructor(port: InsightsPort) { this.#port = port }
  snapshot = (): InsightsState => this.#state
  subscribe = (listener: () => void): (() => void) => { this.#listeners.add(listener); return () => this.#listeners.delete(listener) }

  async load(period: InsightsPeriod): Promise<void> {
    const generation = ++this.#generation
    const cached = this.#cache.get(period)
    if (cached) {
      // Cache hit: the screen answers now, and a background re-read keeps it
      // honest. No skeleton for data we already know.
      this.#set({ ...this.#state, phase: 'ready', period, report: cached.report, recent: cached.recent, recentAvailable: cached.recentAvailable, error: '' })
      void this.refresh()
      return
    }
    this.#set({ ...this.#state, phase: 'loading', period, report: period === this.#state.period ? this.#state.report : null, recent: period === this.#state.period ? this.#state.recent : [], recentAvailable: period === this.#state.period ? this.#state.recentAvailable : 0, error: '' })
    try {
      const [report, recent, catalog] = await Promise.all([
        this.#port.report(period),
        this.#port.recent(period),
        this.#state.pricesPhase === 'idle' ? this.#port.prices() : Promise.resolve({ prices: this.#state.prices, currency: this.#state.pricesCurrency }),
      ])
      this.#cache.set(period, { report, recent: recent.rows, recentAvailable: recent.available })
      if (generation === this.#generation) this.#set({ phase: 'ready', period, report, recent: recent.rows, recentAvailable: recent.available, prices: catalog.prices, pricesCurrency: catalog.currency, pricesPhase: 'ready', error: '' })
    } catch {
      if (generation === this.#generation) this.#set({ ...this.#state, phase: 'error', error: 'Insights are unavailable' })
    }
  }

  /**
   * The scheduled re-read behind the auto-refresh: the report on screen stays
   * put while the next one loads — the phase never flips to loading, so no
   * skeleton, no disabled period buttons, no flicker. A failed cycle keeps the
   * current numbers and is retried by the next tick. An errored page escalates
   * to a real load instead of no-oping forever.
   */
  async refresh(): Promise<void> {
    if (this.#state.phase === 'error') {
      await this.load(this.#state.period)
      return
    }
    if (this.#state.phase !== 'ready' || this.#refreshing) return
    const generation = this.#generation
    const period = this.#state.period
    this.#refreshing = true
    try {
      const [report, recent] = await Promise.all([this.#port.report(period), this.#port.recent(period)])
      // A load() that started while this read was in flight owns the screen.
      if (generation === this.#generation) {
        this.#cache.set(period, { report, recent: recent.rows, recentAvailable: recent.available })
        this.#set({ ...this.#state, report, recent: recent.rows, recentAvailable: recent.available, error: '' })
      }
    } catch {
      // Quiet by contract: the visible report stays the latest known truth.
    } finally {
      this.#refreshing = false
    }
  }

  /** True when the price reached the catalog; false leaves the editor open. */
  async savePrice(draft: PriceDraft): Promise<boolean> {
    const generation = this.#generation
    // A price edit changes every period's cost: the cache is the old prices
    // and may not answer for them.
    this.#cache.clear()
    this.#set({ ...this.#state, pricesPhase: 'saving', error: '' })
    try {
      const catalog = await this.#port.setPrice(draft)
      if (generation === this.#generation) this.#set({ ...this.#state, prices: catalog.prices, pricesCurrency: catalog.currency, pricesPhase: 'ready' })
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
    this.#cache.clear()
    this.#set({ ...this.#state, pricesPhase: 'saving', error: '' })
    try {
      const catalog = await this.#port.removePrice(model)
      if (generation === this.#generation) this.#set({ ...this.#state, prices: catalog.prices, pricesCurrency: catalog.currency, pricesPhase: 'ready' })
      await this.load(this.#state.period)
      return true
    } catch {
      if (generation === this.#generation) this.#set({ ...this.#state, pricesPhase: 'error', error: 'The price was not removed' })
      return false
    }
  }

  /** The unit of account the rates are written in; relabels every cost figure. */
  async setCurrency(currency: string): Promise<boolean> {
    const generation = this.#generation
    this.#set({ ...this.#state, pricesPhase: 'saving', error: '' })
    try {
      const catalog = await this.#port.setCurrency(currency)
      if (generation === this.#generation) this.#set({ ...this.#state, prices: catalog.prices, pricesCurrency: catalog.currency, pricesPhase: 'ready' })
      return true
    } catch {
      if (generation === this.#generation) this.#set({ ...this.#state, pricesPhase: 'error', error: 'The currency was not saved' })
      return false
    }
  }

  dispose(): void { this.#listeners.clear() }
  #set(state: InsightsState): void { this.#state = state; for (const listener of this.#listeners) listener() }
}
