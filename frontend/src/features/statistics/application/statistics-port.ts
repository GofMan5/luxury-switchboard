import type { StatisticsPeriod, StatisticsSnapshot } from '../domain/statistics'

export interface StatisticsPort {
  load(period: StatisticsPeriod, signal?: AbortSignal): Promise<StatisticsSnapshot>
}
