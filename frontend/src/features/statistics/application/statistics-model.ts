import type { StatisticsPeriod, StatisticsSnapshot } from '../domain/statistics'
import type { StatisticsPort } from './statistics-port'

export interface StatisticsState {
  readonly phase: 'idle' | 'loading' | 'ready' | 'error'
  readonly period: StatisticsPeriod
  readonly snapshot: StatisticsSnapshot | null
  readonly error: string
}

export class StatisticsModel {
  readonly #port: StatisticsPort
  #state: StatisticsState = { phase: 'idle', period: '24h', snapshot: null, error: '' }
  #listeners = new Set<() => void>()
  #generation = 0
  constructor(port: StatisticsPort) { this.#port = port }
  snapshot = (): StatisticsState => this.#state
  subscribe = (listener: () => void): (() => void) => { this.#listeners.add(listener); return () => this.#listeners.delete(listener) }
  async load(period: StatisticsPeriod): Promise<void> {
    const generation = ++this.#generation
    this.#set({ ...this.#state, phase: 'loading', period, snapshot: period === this.#state.period ? this.#state.snapshot : null, error: '' })
    try {
      const snapshot = await this.#port.load(period)
      if (generation === this.#generation) this.#set({ phase: 'ready', period, snapshot, error: '' })
    } catch {
      if (generation === this.#generation) this.#set({ ...this.#state, phase: 'error', error: 'Statistics are unavailable' })
    }
  }
  dispose(): void { this.#listeners.clear() }
  #set(state: StatisticsState): void { this.#state = state; for (const listener of this.#listeners) listener() }
}
