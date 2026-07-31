import type { ControlPlaneSession } from '../../../platform/stdio/session'
import type { ActivityRequest } from '../../activity/domain/activity'
import type { StatisticsPort } from '../application/statistics-port'
import type { HistoryStats, StatisticsPeriod, StatisticsSnapshot } from '../domain/statistics'

export class StdioStatisticsPort implements StatisticsPort {
  readonly #session: ControlPlaneSession
  constructor(session: ControlPlaneSession) { this.#session = session }
  async load(period: StatisticsPeriod, signal?: AbortSignal): Promise<StatisticsSnapshot> {
    const [stats, recent] = await Promise.all([
      this.#session.call<HistoryStats>('history.stats', { period }, signal),
      this.#session.call<{ requests: readonly ActivityRequest[] }>('history.recent', { period, limit: 100 }, signal),
    ])
    return { stats, recent: recent.requests }
  }
}
