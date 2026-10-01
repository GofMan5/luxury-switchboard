import { useEffect, useState } from 'react'
import { ArrowRight, Check, Clock3, Copy, Gauge } from 'lucide-react'
import { useActivity } from '../../activity/ui/useActivity'
import { activityLabels } from '../../activity/ui/activity-view'
import { useProviders } from '../../providers/ui/useProviders'
import { useRelay } from '../../relay/ui/useRelay'
import { useInsights } from '../../insights/ui/useInsights'
import { formatClock, formatCost, formatDecimal, formatDuration, formatInteger } from '../../../shared/format/metrics'
import { StatusDot } from '../../../shared/ui/StatusDot'
import styles from './OverviewPage.module.css'

export default function OverviewPage() {
  const activity = useActivity()
  const { state: providers } = useProviders()
  const { state: relay } = useRelay()
  const { model: insights, state: insightsState } = useInsights()
  // The same model the Insights tab reads: one source of truth, and opening
  // the tab after this is instant because the report is already in memory.
  // Only a first visit loads it — a report already held is fresh enough, and
  // every remount refetching it bought nothing but a round-trip.
  useEffect(() => { if (insightsState.phase === 'idle') void insights.load('24h') }, [insights, insightsState.phase])
  const summary = activity.summary
  const recent = activity.requests.slice(0, 7)
  const overview = insightsState.report?.overview
  const address = relay.snapshot.state === 'live' ? relay.snapshot.address : ''
  const [copied, setCopied] = useState(false)
  const copyAddress = async () => {
    if (!address) return
    try {
      await navigator.clipboard.writeText(address)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 2000)
    } catch { /* clipboard is a courtesy, not a guarantee */ }
  }

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

      <section className={styles.today} aria-label="Last 24 hours">
        <header>
          <div>
            <h2>Last 24 hours</h2>
            <span>{insightsState.phase === 'loading' || insightsState.phase === 'idle' ? 'Reading history…' : overview ? 'From the persisted request history' : 'History is unavailable'}</span>
          </div>
          <button type="button" className={styles.linkButton} onClick={() => { window.location.hash = 'insights' }}>
            Open Insights <ArrowRight size={14} aria-hidden="true" />
          </button>
        </header>
        <div className={styles.todayGrid}>
          <Metric label="Requests" value={overview ? formatInteger(overview.volume.requests) : '—'} />
          <Metric label="Completed" value={overview ? formatInteger(overview.volume.completed) : '—'} />
          <Metric label="Success rate" value={overview ? `${Math.round(overview.successRate * 100)}%` : '—'} />
          <Metric
            label="Estimated cost"
            value={overview ? (overview.volume.isPriced ? formatCost(overview.volume.cost) : overview.volume.cost > 0 ? `≥ ${formatCost(overview.volume.cost)}` : '—') : '—'}
          />
          <Metric label="Tokens" value={overview ? formatInteger(overview.volume.totalTokens) : '—'} />
        </div>
      </section>

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
                    <span>{provider.rpm === 0 ? 'Unlimited' : `${provider.rpm} per ${provider.rateUnit === 'second' ? 'second' : 'minute'}`} · {provider.authMode === 'passthrough' ? 'Passthrough' : provider.keyCount > 0 ? 'Key configured' : 'No key'}</span>
                  </div>
                  <span className={active ? styles.active : styles.standby}>{active ? 'Active' : 'Standby'}</span>
                </div>
              )
            })}
          </div>
          <div className={styles.relayFoot}>
            <span>Listener</span>
            <span className={styles.listenerValue}>
              <strong>{relay.snapshot.state === 'live' ? relay.snapshot.address : relay.snapshot.state === 'error' ? 'Error' : 'Stopped'}</strong>
              {address ? (
                <button
                  type="button"
                  className={styles.copyButton}
                  onClick={() => void copyAddress()}
                  aria-label={copied ? 'Relay address copied' : `Copy relay address ${address}`}
                  disabled={copied}
                >
                  {copied ? <Check size={13} aria-hidden="true" /> : <Copy size={13} aria-hidden="true" />}
                </button>
              ) : null}
            </span>
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
