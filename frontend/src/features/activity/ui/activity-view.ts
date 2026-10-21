import type { ActivityRequest, ActivityState } from '../domain/activity'

export const activityLabels: Record<ActivityState, string> = {
  active: 'Streaming',
  retrying: 'Retrying',
  completed: 'Complete',
  failed: 'Error',
  cancelled: 'Cancelled',
}

/** The row's state as the operator reads it: a live request whose provider is
 * still reasoning is thinking, not yet streaming its answer. */
export function activityLabel(request: Pick<ActivityRequest, 'state' | 'thinking'>): string {
  return request.state === 'active' && request.thinking ? 'Thinking' : activityLabels[request.state]
}
