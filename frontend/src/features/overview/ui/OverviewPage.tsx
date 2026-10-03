import { useEffect, useState } from 'react'
import { ArrowRight, Check, Clock3, Copy } from 'lucide-react'
import { useActivity } from '../../activity/ui/useActivity'
import { activityLabels } from '../../activity/ui/activity-view'
import { useProviders } from '../../providers/ui/useProviders'
import { useRelay } from '../../relay/ui/useRelay'
import { useInsights } from '../../insights/ui/useInsights'
import { formatClock, formatCost, formatDecimal, formatDuration, formatInteger } from '../../../shared/format/metrics'
import { Metric, MetricStrip, Panel, Pill } from '../../../shared/ui/chrome'
import { StatusDot } from '../../../shared/ui/StatusDot'
import styles from './OverviewPage.module.css'

/** The report re-reads itself on this cadence while the workspace is open. */
const INSIGHTS_REFRESH_MS = 60_000

/** Lower sorts first: the provider serving traffic, then the ones that could
 * serve right now, then the configured-but-idle shells. */
function relevance(provider: { id: string; enabled: boolean; keyCount: number; authMode: string }, activeId: string): number {
  if (provider.id === activeId) return 0
  if (provider.enabled && (provider.keyCount > 0 || provider.authMode === 'passthrough')) return 1
  if (provider.enabled) return 2
  return 3
}

export default function OverviewPage() {
  const activity = useActivity()
  const { state: providers } = useProviders()
  const { state: relay } = useRelay()
  const { model: insights, state: insightsState } = useInsights()
  // The same model the Insights tab reads: one source of truth, and opening
  // the tab after this is instant because the report is already in memory.
  // Only a first visit loads it — a report already held is fresh enough, and
  // every remount refetching it bought nothing but a round-trip.
  // The card says "Last 24 hours", so it owns the period: a visit after the
  // Insights tab picked another range re-reads the report for 24h instead of
  // showing all-time numbers under the wrong heading.
  useEffect(() => {
    if (insightsState.phase === 'idle' || insightsState.period !== '24h') void insights.load('24h')
  }, [insights, insightsState.phase, insightsState.period])
  // The quiet re-read: refresh() swaps the numbers in place, so the page stays
  // still while the figures stay honest.
  useEffect(() => {
    const timer = window.setInterval(() => void insights.refresh(), INSIGHTS_REFRESH_MS)
    return () => window.clearInterval(timer)
  }, [insights])
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
        <span className={styles.windowLabel}><Clock3 size={14} aria-hidden="true" />Live · 60 sec window</span>
      </header>

      <div className="page-body">
        <MetricStrip>
          <Metric label="Requests / min" value={formatDecimal(summary.rpm)} />
          <Metric label="Active" value={String(summary.active)} tone={summary.active > 0 ? 'success' : undefined} />
          <Metric label="Queued" value={String(summary.queued)} tone={summary.queued > 0 ? 'warning' : undefined} />
          <Metric label="Success rate" value={`${formatDecimal(summary.successRate)}%`} />
          <Metric label="p95 latency" value={formatDuration(summary.p95Ms)} />
        </MetricStrip>

        <div className={styles.columns}>
          <Panel
            title="Recent activity"
            subtitle={activity.phase === 'loading' ? 'Connecting…' : `${activity.requests.length} requests in memory`}
            actions={<button type="button" className={styles.linkButton} onClick={() => { window.location.hash = 'activity' }}>View all <ArrowRight size={13} aria-hidden="true" /></button>}
          >
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
                      <td className={styles.numeric}>{request.status || '—'}</td>
                      <td className={styles.numeric}>{formatDuration(request.latencyMs)}</td>
                      <td className={styles.numeric}>{formatClock(request.updatedAt)}</td>
                    </tr>
                  ))}
                  {recent.length === 0 ? (
                    <tr><td colSpan={6} className={styles.empty}>Requests will appear here as soon as the relay receives traffic.</td></tr>
                  ) : null}
                </tbody>
              </table>
            </div>
          </Panel>

          <div className={styles.rail}>
            <Panel
              title="Last 24 hours"
              label="Last 24 hours"
              subtitle={insightsState.phase === 'loading' || insightsState.phase === 'idle' ? 'Reading history…' : overview ? `Updated ${formatClock(insightsState.report!.generatedAt)} · auto-refresh every minute` : 'History is unavailable'}
              actions={<button type="button" className={styles.linkButton} onClick={() => { window.location.hash = 'insights' }}>Insights <ArrowRight size={13} aria-hidden="true" /></button>}
            >
              <div className={styles.dailyGrid}>
                <div className={styles.dailyMetric}><span>Requests</span><strong>{overview ? formatInteger(overview.volume.requests) : '—'}</strong></div>
                <div className={styles.dailyMetric}><span>Completed</span><strong>{overview ? formatInteger(overview.volume.completed) : '—'}</strong></div>
                <div className={styles.dailyMetric}><span>Success</span><strong>{overview ? `${Math.round(overview.successRate * 100)}%` : '—'}</strong></div>
                <div className={styles.dailyMetric}><span>Est. cost</span><strong>{overview ? (overview.volume.isPriced ? formatCost(overview.volume.cost) : overview.volume.cost > 0 ? `≥ ${formatCost(overview.volume.cost)}` : '—') : '—'}</strong></div>
                <div className={styles.dailyMetric}><span>Tokens</span><strong>{overview ? formatInteger(overview.volume.totalTokens) : '—'}</strong></div>
                <div className={styles.dailyMetric}><span>Cached</span><strong>{overview ? formatInteger(overview.volume.cachedTokens) : '—'}</strong></div>
              </div>
            </Panel>

            <Panel
              title="Provider routes"
              subtitle={`${providers.catalog.providers.length} configured · ${providers.catalog.providers.filter((provider) => provider.enabled && (provider.keyCount > 0 || provider.authMode === 'passthrough')).length} routable`}
              actions={<button type="button" className={styles.linkButton} onClick={() => { window.location.hash = 'providers' }}>Providers <ArrowRight size={13} aria-hidden="true" /></button>}
            >
              <div className={styles.providerList}>
                {/* The card answers "who serves traffic": the active provider
                    first, then the routable ones. Disabled shells and keyless
                    entries would only be scrolled past, so they fold away. */}
                {providers.catalog.providers
                  .slice()
                  .sort((left, right) => relevance(left, providers.catalog.activeId) - relevance(right, providers.catalog.activeId))
                  .slice(0, 5)
                  .map((provider) => {
                    const active = provider.id === providers.catalog.activeId
                    const health = providers.health.get(provider.id)
                    return (
                      <div className={styles.providerRow} key={provider.id}>
                        <StatusDot state={health ? (health.up ? 'healthy' : 'failed') : provider.enabled ? 'healthy' : 'stopped'} />
                        <div>
                          <strong>{provider.name}</strong>
                          <span>{provider.rpm === 0 ? 'Unlimited' : `${provider.rpm} per ${provider.rateUnit === 'second' ? 'second' : 'minute'}`} · {provider.authMode === 'passthrough' ? 'Passthrough' : provider.keyCount > 0 ? `${provider.keyCount} key${provider.keyCount === 1 ? '' : 's'}` : 'No key'}</span>
                        </div>
                        {active ? <Pill tone="info">Active</Pill> : <Pill>Standby</Pill>}
                      </div>
                    )
                  })}
              </div>
              {providers.catalog.providers.length > 5 ? (
                <div className={styles.providerMore}>
                  <span>{providers.catalog.providers.length - 5} more</span>
                  <button type="button" onClick={() => { window.location.hash = 'providers' }}>Open Providers</button>
                </div>
              ) : null}
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
            </Panel>
          </div>
        </div>
      </div>
    </section>
  )
}
