import { ArrowRight, Clock3, Gauge } from 'lucide-react'
import { useActivity } from '../../activity/ui/useActivity'
import { activityLabels } from '../../activity/ui/activity-view'
import { useProviders } from '../../providers/ui/useProviders'
import { useRelay } from '../../relay/ui/useRelay'
import { formatClock, formatDecimal, formatDuration } from '../../../shared/format/metrics'
import { StatusDot } from '../../../shared/ui/StatusDot'
import styles from './OverviewPage.module.css'

export default function OverviewPage() {
  const activity = useActivity()
  const { state: providers } = useProviders()
  const { state: relay } = useRelay()
  const summary = activity.summary
  const recent = activity.requests.slice(0, 7)

  return (
    <section className={styles.page}>
      <header className="page-header">
        <div>
          <h1>Overview</h1>
          <p>Local relay operations</p>
        </div>
        <span className={styles.windowLabel}><Clock3 size={15} aria-hidden="true" />Last 60 sec</span>
      </header>

      <div className={styles.metrics} aria-label="Relay summary">
        <Metric label="Requests / min" value={formatDecimal(summary.rpm)} />
        <Metric label="Active" value={String(summary.active)} />
        <Metric label="Queued" value={String(summary.queued)} />
        <Metric label="Success rate" value={`${formatDecimal(summary.successRate)}%`} />
        <Metric label="p95 latency" value={formatDuration(summary.p95Ms)} />
      </div>

      <div className={styles.grid}>
        <section className={styles.activityPane} aria-labelledby="recent-title">
          <div className={styles.sectionHeader}>
            <div>
              <h2 id="recent-title">Recent activity</h2>
              <span>{activity.phase === 'loading' ? 'Connecting…' : `${activity.requests.length} requests in memory`}</span>
            </div>
            <button type="button" className={styles.linkButton} onClick={() => { window.location.hash = 'activity' }}>
              View all <ArrowRight size={14} aria-hidden="true" />
            </button>
          </div>
          <div className={styles.tableWrap}>
            <table className={styles.table}>
              <thead>
                <tr>
                  <th>State</th>
                  <th>Model</th>
                  <th>Route</th>
                  <th>HTTP</th>
                  <th>Latency</th>
                  <th>Time</th>
                </tr>
              </thead>
              <tbody>
                {recent.map((request) => (
                  <tr key={request.id}>
                    <td><span className={styles.state}><StatusDot state={request.state} />{activityLabels[request.state]}</span></td>
                    <td title={request.model}>{request.model || '—'}</td>
                    <td>{request.providerName || '—'}</td>
                    <td>{request.status || '—'}</td>
                    <td>{formatDuration(request.latencyMs)}</td>
                    <td>{formatClock(request.updatedAt)}</td>
                  </tr>
                ))}
                {recent.length === 0 ? (
                  <tr><td colSpan={6} className={styles.empty}>Requests will appear here as soon as the relay receives traffic.</td></tr>
                ) : null}
              </tbody>
            </table>
          </div>
        </section>

        <section className={styles.healthPane} aria-labelledby="health-title">
          <div className={styles.sectionHeader}>
            <div>
              <h2 id="health-title">Provider routes</h2>
              <span>{providers.catalog.providers.length} configured</span>
            </div>
            <Gauge size={18} aria-hidden="true" />
          </div>
          <div className={styles.providerList}>
            {providers.catalog.providers.map((provider) => {
              const active = provider.id === providers.catalog.activeId
              return (
                <div className={styles.providerRow} key={provider.id}>
                  <StatusDot state={provider.enabled ? 'healthy' : 'stopped'} />
                  <div>
                    <strong>{provider.name}</strong>
                    <span>{provider.rpm === 0 ? 'Unlimited' : `${provider.rpm} RPM`} · {provider.authMode === 'passthrough' ? 'Passthrough' : provider.keyCount > 0 ? 'Key configured' : 'No key'}</span>
                  </div>
                  <span className={active ? styles.active : styles.standby}>{active ? 'Active' : 'Standby'}</span>
                </div>
              )
            })}
          </div>
          <div className={styles.relayFoot}>
            <span>Listener</span>
            <strong>{relay.snapshot.state === 'live' ? relay.snapshot.address : relay.snapshot.state === 'error' ? 'Error' : 'Stopped'}</strong>
          </div>
        </section>
      </div>
    </section>
  )
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <div className={styles.metric}>
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  )
}
