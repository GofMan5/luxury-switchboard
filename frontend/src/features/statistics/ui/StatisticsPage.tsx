import { useEffect } from 'react'
import { RefreshCw } from 'lucide-react'
import { formatClock, formatDecimal, formatDuration } from '../../../shared/format/metrics'
import { Button } from '../../../shared/ui/Button'
import { StatusDot } from '../../../shared/ui/StatusDot'
import type { ActivityState } from '../../activity/domain/activity'
import type { StatisticsPeriod } from '../domain/statistics'
import { useStatistics } from './useStatistics'
import styles from './StatisticsPage.module.css'

const periods: readonly StatisticsPeriod[] = ['24h', '48h', '72h', 'all']

// Labels live here instead of the activity slice: this page renders persisted
// history rows, and reaching into another slice's ui module for a display map
// couples the two workspaces for no reason.
const stateLabels: Record<ActivityState, string> = {
  active: 'Streaming',
  retrying: 'Retrying',
  completed: 'Complete',
  failed: 'Error',
  cancelled: 'Cancelled',
}

const skeletonMetrics = ['Requests', 'p95 latency', 'Processed tokens', 'Cache / reasoning', 'Generation speed']
const skeletonRows = [0, 1, 2]
// List rows stay short: a persisted diagnostic can run to thousands of runes,
// and rendering it in full once per error row floods the DOM. The cell keeps
// the full text behind its tooltip.
const errorPreviewLimit = 180

export default function StatisticsPage() {
  const { model, state } = useStatistics()
  useEffect(() => { if (state.phase === 'idle') void model.load('24h') }, [model, state.phase])
  const stats = state.snapshot?.stats
  const loading = state.phase === 'loading' || state.phase === 'idle'
  // Placeholders whenever there is nothing measured to show — including a
  // first-load failure, where zeros would read as a real measurement.
  const showSkeleton = !stats
  return (
    <section className={styles.page}>
      <header className="page-header">
        <div><h1>Statistics</h1><p>Persistent sanitized history, latency and token accounting</p></div>
        <div className={styles.actions}>
          <div className={styles.periods} role="group" aria-label="Statistics period">
            {periods.map((period) => (
              <button
                key={period}
                type="button"
                data-active={state.period === period}
                aria-pressed={state.period === period}
                disabled={loading}
                onClick={() => void model.load(period)}
              >
                {period === 'all' ? 'All' : period}
              </button>
            ))}
          </div>
          <Button disabled={loading} onClick={() => void model.load(state.period)}>
            <RefreshCw size={15} aria-hidden="true" />{loading ? 'Refreshing…' : 'Refresh'}
          </Button>
        </div>
      </header>
      {state.error ? <div className={styles.error} role="alert">{state.error}</div> : null}
      {showSkeleton && loading ? <p className={styles.loading} role="status">Loading statistics…</p> : null}
      <div className={styles.metrics} aria-busy={loading}>
        {showSkeleton
          ? skeletonMetrics.map((label) => (
            <div key={label} className={styles.metric} aria-hidden="true"><span>{label}</span><strong className={styles.skeleton} /></div>
          ))
          : (
            <>
              <Metric label="Requests" value={formatInteger(stats?.requests)} detail={`${formatInteger(stats?.completed)} completed · ${formatInteger(stats?.failed)} failed`} />
              <Metric label="p95 latency" value={formatDuration(stats?.p95Ms ?? 0)} detail={`${formatInteger(stats?.cancelled)} cancelled`} />
              <Metric label="Processed tokens" value={formatInteger(stats?.processedTokens)} detail={`${formatInteger(stats?.nonCachedTokens)} non-cached`} />
              <Metric label="Cache / reasoning" value={formatInteger(stats?.cachedTokens)} detail={`${formatInteger(stats?.reasoningTokens)} reasoning subset`} />
              <Metric label="Generation speed" value={stats?.tokensPerSecond ? `${formatDecimal(stats.tokensPerSecond)} tok/s` : '—'} detail={`${formatInteger(stats?.retries)} retries`} />
            </>
          )}
      </div>
      <section className={styles.history}>
        <header><div><h2>Recent persisted requests</h2><span>{state.snapshot ? `${state.snapshot.recent.length} rows in selected period` : 'No data yet'}</span></div></header>
        <div className={styles.tableWrap} aria-busy={loading}><table aria-label="Recent persisted requests"><thead><tr><th scope="col">State</th><th scope="col">Model</th><th scope="col">Provider</th><th scope="col">HTTP</th><th scope="col">Latency</th><th scope="col">Processed</th><th scope="col">Cached</th><th scope="col">Time</th></tr></thead><tbody>
          {showSkeleton
            ? skeletonRows.map((row) => <tr key={row} className={styles.skeletonRow} aria-hidden="true"><td colSpan={8}><span className={styles.skeleton} /></td></tr>)
            : state.snapshot?.recent.map((request) => <tr key={request.id}><td><span className={styles.state}><StatusDot state={request.state} />{stateLabels[request.state]}</span></td><td>{request.model || '—'}</td><td>{request.providerId || '—'}</td><td>{request.status || '—'}</td><td>{formatDuration(request.latencyMs)}</td><td>{formatInteger(request.totalTokens)}</td><td>{formatInteger(request.cachedTokens)}</td><td>{formatClock(request.updatedAt)}</td></tr>)}
          {!showSkeleton && !loading && !state.snapshot?.recent.length ? <tr><td colSpan={8} className={styles.empty}>No persisted requests in this period.</td></tr> : null}
        </tbody></table></div>
      </section>
      {state.snapshot?.recent.some((request) => request.errorDetail || request.errorCode) ? (
        <section className={styles.history} aria-label="Request errors">
          <header><div><h2>Request errors</h2><span>Full diagnostics from persisted requests</span></div></header>
          <div className={styles.errorList}>
            {state.snapshot?.recent.filter((request) => request.errorDetail || request.errorCode).map((request) => {
              const full = request.errorDetail || request.errorCode || ''
              return (
                <article key={request.id} className={styles.errorItem}>
                  <strong title={full} tabIndex={0} aria-label={full}>{previewError(full)}</strong>
                  <small>{request.model || 'Unknown model'} - {formatClock(request.updatedAt)}</small>
                </article>
              )
            })}
          </div>
        </section>
      ) : null}
    </section>
  )
}

function Metric({ label, value, detail }: { label: string; value: string; detail: string }) {
  return <div className={styles.metric}><span>{label}</span><strong>{value}</strong><small>{detail}</small></div>
}

function previewError(detail: string): string {
  return detail.length > errorPreviewLimit ? `${detail.slice(0, errorPreviewLimit)}…` : detail
}

function formatInteger(value: number | undefined): string {
  return (value ?? 0).toLocaleString()
}
