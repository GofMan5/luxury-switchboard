import type { ActivityState } from '../../features/activity/domain/activity'

type SemanticState = ActivityState | 'live' | 'stopped' | 'error' | 'healthy' | 'degraded'

const toneByState: Record<SemanticState, string> = {
  active: 'info',
  retrying: 'warning',
  completed: 'success',
  failed: 'danger',
  cancelled: 'neutral',
  live: 'success',
  healthy: 'success',
  degraded: 'warning',
  stopped: 'neutral',
  error: 'danger',
}

export function StatusDot({ state }: { state: SemanticState }) {
  return <span className="status-dot" data-tone={toneByState[state]} aria-hidden="true" />
}
