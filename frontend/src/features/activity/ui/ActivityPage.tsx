import { useDeferredValue, useMemo, useState } from 'react'
import { Copy, Pause, Play, Search, SlidersHorizontal, X } from 'lucide-react'
import { useAppServices } from '../../../app/services'
import { formatBytes, formatClock, formatDecimal, formatDuration } from '../../../shared/format/metrics'
import { Button } from '../../../shared/ui/Button'
import { StatusDot } from '../../../shared/ui/StatusDot'
import { useModalFocus } from '../../../shared/ui/useModalFocus'
import type { ActivityRequest, ActivityState } from '../domain/activity'
import { activityLabels } from './activity-view'
import { useActivity } from './useActivity'
import styles from './ActivityPage.module.css'

type StateFilter = 'all' | ActivityState

const skeletonRows = [0, 1, 2, 3, 4, 5]

export default function ActivityPage() {
  const activity = useActivity()
  const { activity: activityModel } = useAppServices()
  const [search, setSearch] = useState('')
  const deferredSearch = useDeferredValue(search.trim().toLocaleLowerCase())
  const [stateFilter, setStateFilter] = useState<StateFilter>('all')
  const [paused, setPaused] = useState(false)
  const [frozen, setFrozen] = useState<readonly ActivityRequest[]>([])
  const [selectedID, setSelectedID] = useState('')
  const [inspectorOpen, setInspectorOpen] = useState(false)
  const source = paused ? frozen : activity.requests
  const requests = useMemo(
    () => source.filter((request) => {
      if (stateFilter !== 'all' && request.state !== stateFilter) return false
      if (!deferredSearch) return true
      return [request.model, request.providerName, request.method, request.path, request.id]
        .some((value) => value.toLocaleLowerCase().includes(deferredSearch))
    }),
    [source, stateFilter, deferredSearch],
  )
  const selected = source.find((request) => request.id === selectedID)
  const loading = activity.phase === 'loading' && activity.requests.length === 0
  const failed = activity.phase === 'error'

  const togglePause = () => {
    if (!paused) setFrozen(activity.requests)
    setPaused((value) => !value)
  }

  const openRequest = (id: string) => {
    setSelectedID(id)
    setInspectorOpen(true)
  }

  return (
    <section className={styles.page}>
      <header className="page-header">
        <div>
          <h1>Live Activity</h1>
          <p>Sanitized request lifecycle and performance</p>
        </div>
        <span className={styles.liveState}>
          <StatusDot state={paused ? 'stopped' : 'active'} />
          {paused ? 'Visual updates paused' : 'Updating live'}
        </span>
      </header>

      <div className={styles.toolbar}>
        <label className={styles.search}>
          <Search size={16} aria-hidden="true" />
          <span className="sr-only">Search requests</span>
          <input
            type="search"
            value={search}
            placeholder="Search model, route, request…"
            onChange={(event) => setSearch(event.currentTarget.value)}
          />
        </label>
        <label className={styles.filter}>
          <SlidersHorizontal size={15} aria-hidden="true" />
          <span className="sr-only">Filter state</span>
          <select value={stateFilter} onChange={(event) => setStateFilter(event.currentTarget.value as StateFilter)}>
            <option value="all">All states</option>
            <option value="active">Streaming</option>
            <option value="retrying">Retrying</option>
            <option value="completed">Complete</option>
            <option value="failed">Error</option>
            <option value="cancelled">Cancelled</option>
          </select>
        </label>
        <Button variant="secondary" onClick={togglePause}>
          {paused ? <Play size={15} aria-hidden="true" /> : <Pause size={15} aria-hidden="true" />}
          {paused ? 'Resume' : 'Pause'}
        </Button>
        <Button variant="primary" disabled={!selected} onClick={() => setInspectorOpen(true)}>Details</Button>
      </div>

      {failed ? (
        <div className={styles.error} role="alert">
          <span>{activity.error || 'Activity is unavailable'}</span>
          <Button variant="secondary" onClick={() => void activityModel.connect()}>Retry</Button>
        </div>
      ) : null}

      <div className={styles.body} data-inspector={Boolean(selected && inspectorOpen)}>
        <div className={styles.tablePane} aria-busy={loading}>
          {loading ? <p className="sr-only" role="status">Loading live activity…</p> : null}
          <table className={styles.table} aria-label="Live requests">
            <thead>
              <tr>
                <th scope="col">State</th>
                <th scope="col">Model</th>
                <th scope="col">Route / Provider</th>
                <th scope="col">HTTP</th>
                <th scope="col">Queue</th>
                <th scope="col">Latency</th>
                <th scope="col">Tok/s</th>
                <th scope="col">Context</th>
                <th scope="col">Traffic</th>
                <th scope="col">Time</th>
              </tr>
            </thead>
            <tbody>
              {loading ? skeletonRows.map((row) => (
                <tr key={row} className={styles.skeletonRow} aria-hidden="true">
                  <td colSpan={10}><span className={styles.skeleton} /></td>
                </tr>
              )) : requests.map((request, index) => {
                const isSelected = request.id === selectedID
                return (
                  <tr
                    key={request.id}
                    data-selected={isSelected}
                    data-row=""
                    aria-selected={isSelected}
                    tabIndex={isSelected || (selectedID === '' && index === 0) ? 0 : -1}
                    onClick={() => openRequest(request.id)}
                    onKeyDown={(event) => {
                      if (event.key === 'Enter' || event.key === ' ') {
                        event.preventDefault()
                        openRequest(request.id)
                        return
                      }
                      if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
                        event.preventDefault()
                        const rows = [...(event.currentTarget.closest('tbody')?.querySelectorAll<HTMLElement>('tr[data-row]') ?? [])]
                        const next = rows[rows.indexOf(event.currentTarget) + (event.key === 'ArrowDown' ? 1 : -1)]
                        next?.focus()
                      }
                    }}
                  >
                    <td><span className={styles.state}><StatusDot state={request.state} />{activityLabels[request.state]}</span></td>
                    <td title={request.model}>{request.model || '—'}</td>
                    <td>{request.providerName || '—'}</td>
                    <td>{request.status || '—'}</td>
                    <td>{request.queueMs > 0 ? formatDuration(request.queueMs) : '0 ms'}</td>
                    <td>{formatDuration(request.latencyMs)}</td>
                    <td>{request.tokensPerSecond > 0 ? formatDecimal(request.tokensPerSecond) : '—'}</td>
                    <td>{request.contextTokens > 0 ? request.contextTokens.toLocaleString() : '—'}</td>
                    <td>{formatBytes(request.bytesIn + request.bytesOut)}</td>
                    <td>{formatClock(request.updatedAt)}</td>
                  </tr>
                )
              })}
              {!loading && !failed && requests.length === 0 ? (
                <tr><td colSpan={10} className={styles.empty}>No requests match the current filters.</td></tr>
              ) : null}
            </tbody>
          </table>
          <footer className={styles.tableFooter}>
            <span>
              {loading ? 'Connecting…' : activity.available > activity.requests.length
                ? `Newest ${activity.requests.length} of ${activity.available}`
                : `Showing ${requests.length} of ${activity.requests.length}`}
            </span>
            <span>Live buffer · 100 visible</span>
          </footer>
        </div>
        {selected && inspectorOpen ? <RequestInspector request={selected} onClose={() => setInspectorOpen(false)} /> : null}
      </div>
    </section>
  )
}

function RequestInspector({ request, onClose }: { request: ActivityRequest; onClose: () => void }) {
  const dialogRef = useModalFocus<HTMLElement>(onClose)
  // Non-modal side panel: live updates continue behind it, so modal semantics would lie.
  return (
    <aside ref={dialogRef} className={styles.inspector} role="complementary" aria-label="Request inspector">
      <header>
        <div>
          <h2>Request Inspector</h2>
          <span className={styles.requestID}>{request.id}</span>
        </div>
        <button type="button" className={styles.close} aria-label="Close inspector" onClick={onClose}>
          <X size={17} aria-hidden="true" />
        </button>
      </header>
      <div className={styles.inspectorStatus}>
        <StatusDot state={request.state} />
        <strong>{activityLabels[request.state]}</strong>
      </div>
      <dl className={styles.details}>
        <Detail label="Model" value={request.model || '—'} />
        <Detail label="Route" value={request.providerName || '—'} />
        <Detail label="Method" value={`${request.method} ${request.path}`} mono />
        <Detail label="HTTP" value={request.status ? String(request.status) : '—'} />
        <Detail label="Latency" value={formatDuration(request.latencyMs)} />
        <Detail label="Queue / retry" value={`${formatDuration(request.queueMs)} · ${request.retries}`} />
        <Detail label="Input bytes" value={formatBytes(request.bytesIn)} />
        <Detail label="Output bytes" value={formatBytes(request.bytesOut)} />
        <Detail label="Input tokens" value={request.inputTokens.toLocaleString()} />
        <Detail label="Output tokens" value={request.outputTokens.toLocaleString()} />
        <Detail label="Cached input" value={request.cachedTokens.toLocaleString()} />
        <Detail label="Reasoning" value={request.reasoningTokens.toLocaleString()} />
        <Detail label="Processed" value={request.totalTokens.toLocaleString()} />
        <Detail label="Context" value={request.contextTokens.toLocaleString()} />
        <Detail label="Generation" value={request.tokensPerSecond > 0 ? `${formatDecimal(request.tokensPerSecond)} tok/s` : '—'} />
      </dl>
      <section className={styles.timeline}>
        <h3>Status timeline</h3>
        <TimelineRow label="Received" value={formatClock(request.startedAt)} done />
        <TimelineRow label="Routed" value={request.providerName || '—'} done />
        {request.retries > 0 ? <TimelineRow label="Retried" value={`${request.retries} attempts`} warning /> : null}
        <TimelineRow label={activityLabels[request.state]} value={formatClock(request.updatedAt)} state={request.state} />
      </section>
      {request.errorCode || request.errorDetail ? (
        <div className={styles.safeError}>
          <span>{request.errorCode || 'Error'}</span>
          <strong>{request.errorDetail || request.errorCode}</strong>
          <button type="button" className={styles.copyDetail} onClick={() => void copyDetail(request.errorDetail || request.errorCode || '')}>
            <Copy size={13} aria-hidden="true" />
            Copy detail
          </button>
        </div>
      ) : null}
    </aside>
  )
}

async function copyDetail(value: string): Promise<void> {
  if (!value) return
  try {
    await navigator.clipboard.writeText(value)
  } catch {
    // The detail stays on screen either way; a failed copy is a notice, not a loss.
  }
}

function Detail({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return <><dt>{label}</dt><dd className={mono ? styles.mono : undefined}>{value}</dd></>
}

function TimelineRow({ label, value, done = false, warning = false, state }: { label: string; value: string; done?: boolean; warning?: boolean; state?: ActivityState }) {
  return (
    <div className={styles.timelineRow}>
      <StatusDot state={state ?? (warning ? 'retrying' : done ? 'completed' : 'active')} />
      <span>{label}</span>
      <time>{value}</time>
    </div>
  )
}
