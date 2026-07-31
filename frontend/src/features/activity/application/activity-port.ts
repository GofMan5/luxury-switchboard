import type { ActivityRequest, ActivitySummary } from '../domain/activity'

export interface ActivityPort {
  list(limit: number, signal?: AbortSignal): Promise<readonly ActivityRequest[]>
  summary(signal?: AbortSignal): Promise<ActivitySummary>
  subscribe(listener: (request: ActivityRequest) => void): () => void
}
