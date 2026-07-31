import type { ActivityState } from '../domain/activity'

export const activityLabels: Record<ActivityState, string> = {
  active: 'Streaming',
  retrying: 'Retrying',
  completed: 'Complete',
  failed: 'Error',
  cancelled: 'Cancelled',
}
