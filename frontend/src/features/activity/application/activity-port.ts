import type { ActivityRequest, ActivitySummary } from '../domain/activity'

/** One activity.list answer. `available` is what the control plane held before
 * the frame budget dropped the oldest rows, so a short list shows as a short
 * list instead of passing for the whole buffer. */
export interface ActivityListResult {
  readonly requests: readonly ActivityRequest[]
  readonly available: number
}

export interface ActivityPort {
  list(limit: number, signal?: AbortSignal): Promise<ActivityListResult>
  summary(signal?: AbortSignal): Promise<ActivitySummary>
  subscribe(listener: (request: ActivityRequest) => void): () => void
}
