import { useEffect } from 'react'
import { RefreshCw } from 'lucide-react'
import { formatClock, formatDecimal, formatDuration } from '../../../shared/format/metrics'
import { Button } from '../../../shared/ui/Button'
import { StatusDot } from '../../../shared/ui/StatusDot'
import { activityLabels } from '../../activity/ui/activity-view'
import type { StatisticsPeriod } from '../domain/statistics'
import { useStatistics } from './useStatistics'
import styles from './StatisticsPage.module.css'

const periods: readonly StatisticsPeriod[] = ['24h', '48h', '72h', 'all']

export default function StatisticsPage() {
  const { model, state } = useStatistics()
  useEffect(() => { if (state.phase === 'idle') void model.load('24h') }, [model, state.phase])
  const stats = state.snapshot?.stats
  return (
    <section className={styles.page}>
      <header className="page-header">
        <div><h1>Statistics</h1><p>Persistent sanitized history, latency and token accounting</p></div>
        <div className={styles.actions}>
          <div className={styles.periods} aria-label="Statistics period">
            {periods.map((period) => <button key={period} type="button" data-active={state.period === period} onClick={() => void model.load(period)}>{period === 'all' ? 'All' : period}</button>)}
          </div>
          <Button disabled={state.phase === 'loading'} onClick={() => void model.load(state.period)}><RefreshCw size={15} />Refresh</Button>
        </div>
      </header>
      {state.error ? <div className={styles.error} role="alert">{state.error}</div> : null}
      <div className={styles.metrics}>
        <Metric label="Requests" value={formatInteger(stats?.requests)} detail={`${formatInteger(stats?.completed)} completed · ${formatInteger(stats?.failed)} failed`} />
        <Metric label="p95 latency" value={formatDuration(stats?.p95Ms ?? 0)} detail={`${formatInteger(stats?.cancelled)} cancelled`} />
        <Metric label="Processed tokens" value={formatInteger(stats?.processedTokens)} detail={`${formatInteger(stats?.nonCachedTokens)} non-cached`} />
        <Metric label="Cache / reasoning" value={formatInteger(stats?.cachedTokens)} detail={`${formatInteger(stats?.reasoningTokens)} reasoning subset`} />
        <Metric label="Generation speed" value={stats?.tokensPerSecond ? `${formatDecimal(stats.tokensPerSecond)} tok/s` : '—'} detail={`${formatInteger(stats?.retries)} retries`} />
      </div>
      <section className={styles.history}>
        <header><div><h2>Recent persisted requests</h2><span>{state.snapshot?.recent.length ?? 0} rows in selected period</span></div></header>
        <div className={styles.tableWrap}><table><thead><tr><th>State</th><th>Model</th><th>Provider</th><th>HTTP</th><th>Latency</th><th>Processed</th><th>Cached</th><th>Time</th></tr></thead><tbody>
          {state.snapshot?.recent.map((request) => <tr key={request.id}><td><span className={styles.state}><StatusDot state={request.state} />{activityLabels[request.state]}</span></td><td>{request.model || '—'}</td><td>{request.providerId || '—'}</td><td>{request.status || '—'}</td><td>{formatDuration(request.latencyMs)}</td><td>{formatInteger(request.totalTokens)}</td><td>{formatInteger(request.cachedTokens)}</td><td>{formatClock(request.updatedAt)}</td></tr>)}
          {state.phase !== 'loading' && !state.snapshot?.recent.length ? <tr><td colSpan={8} className={styles.empty}>No persisted requests in this period.</td></tr> : null}
        </tbody></table></div>
      </section>
    </section>
  )
}

function Metric({ label, value, detail }: { label: string; value: string; detail: string }) {
  return <div className={styles.metric}><span>{label}</span><strong>{value}</strong><small>{detail}</small></div>
}

function formatInteger(value: number | undefined): string {
  return (value ?? 0).toLocaleString()
}
