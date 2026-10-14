import type { HistoryRequest, InsightsPeriod, InsightsReport, ModelPrice, PriceCatalog } from '../domain/insights'
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
  #pricesGeneration = 0
  #refreshing = false
  constructor(port: InsightsPort) { this.#port = port }
  snapshot = (): InsightsState => this.#state
  subscribe = (listener: () => void): (() => void) => { this.#listeners.add(listener); return () => this.#listeners.delete(listener) }

  async load(period: InsightsPeriod): Promise<void> {
    const generation = ++this.#generation
    const prices = this.#state.pricesPhase === 'idle' ? this.#loadPrices() : Promise.resolve()
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
      const [report, recent] = await Promise.all([this.#port.report(period), this.#port.recent(period), prices])
      if (generation === this.#generation) {
        this.#cache.set(period, { report, recent: recent.rows, recentAvailable: recent.available })
        this.#set({ ...this.#state, phase: 'ready', period, report, recent: recent.rows, recentAvailable: recent.available, error: '' })
      }
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
    return this.#saveCatalog(() => this.#port.setPrice(draft), 'The price was not saved', true)
  }

  /** True when the model was dropped; false leaves the editor open. */
  async removePrice(model: string): Promise<boolean> {
    return this.#saveCatalog(() => this.#port.removePrice(model), 'The price was not removed', true)
  }

  /** The unit of account the rates are written in; relabels every cost figure. */
  async setCurrency(currency: string): Promise<boolean> {
    return this.#saveCatalog(() => this.#port.setCurrency(currency), 'The currency was not saved', false)
  }

  async #loadPrices(): Promise<void> {
    const generation = ++this.#pricesGeneration
    this.#set({ ...this.#state, pricesPhase: 'loading' })
    try {
      const catalog = await this.#port.prices()
      if (generation === this.#pricesGeneration) this.#set({ ...this.#state, prices: catalog.prices, pricesCurrency: catalog.currency, pricesPhase: 'ready' })
    } catch {
      if (generation === this.#pricesGeneration) this.#set({ ...this.#state, pricesPhase: 'idle' })
      throw new Error('Prices are unavailable')
    }
  }

  async #saveCatalog(operation: () => Promise<PriceCatalog>, error: string, reload: boolean): Promise<boolean> {
    if (this.#state.pricesPhase === 'saving') return false
    const generation = ++this.#pricesGeneration
    this.#set({ ...this.#state, pricesPhase: 'saving', error: '' })
    try {
      const catalog = await operation()
      if (generation !== this.#pricesGeneration) return true
      // Navigation owns reports, not the global catalog. Invalidate only after
      // commit, including any old-price reads that finished during the write.
      this.#cache.clear()
      this.#generation++
      this.#set({ ...this.#state, prices: catalog.prices, pricesCurrency: catalog.currency, pricesPhase: 'ready' })
      if (reload || this.#state.phase === 'loading') await this.load(this.#state.period)
      return true
    } catch {
      if (generation === this.#pricesGeneration) this.#set({ ...this.#state, pricesPhase: 'error', error })
      return false
    }
  }

  dispose(): void { this.#generation++; this.#pricesGeneration++; this.#listeners.clear() }
  #set(state: InsightsState): void { this.#state = state; for (const listener of this.#listeners) listener() }
}
