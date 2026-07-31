import { useDeferredValue, useMemo, useState } from 'react'
import { Pause, Play, Search, SlidersHorizontal, X } from 'lucide-react'
import { formatBytes, formatClock, formatDecimal, formatDuration } from '../../../shared/format/metrics'
import { Button } from '../../../shared/ui/Button'
import { StatusDot } from '../../../shared/ui/StatusDot'
import type { ActivityRequest, ActivityState } from '../domain/activity'
import { activityLabels } from './activity-view'
import { useActivity } from './useActivity'
import styles from './ActivityPage.module.css'

type StateFilter = 'all' | ActivityState

export default function ActivityPage() {
  const activity = useActivity()
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
  const selected = activity.requests.find((request) => request.id === selectedID)

  const togglePause = () => {
    if (!paused) setFrozen(activity.requests)
    setPaused((value) => !value)
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

      <div className={styles.body} data-inspector={Boolean(selected && inspectorOpen)}>
        <div className={styles.tablePane}>
          <table className={styles.table}>
            <thead>
              <tr>
                <th>State</th>
                <th>Model</th>
                <th>Route / Provider</th>
                <th>HTTP</th>
                <th>Queue</th>
                <th>Latency</th>
                <th>Tok/s</th>
                <th>Context</th>
                <th>Traffic</th>
                <th>Time</th>
              </tr>
            </thead>
            <tbody>
              {requests.map((request) => (
                <tr
                  key={request.id}
                  data-selected={request.id === selectedID}
                  tabIndex={0}
                  onClick={() => setSelectedID(request.id)}
                  onDoubleClick={() => {
                    setSelectedID(request.id)
                    setInspectorOpen(true)
                  }}
                  onKeyDown={(event) => {
                    if (event.key === 'Enter' || event.key === ' ') {
                      event.preventDefault()
                      setSelectedID(request.id)
                      setInspectorOpen(true)
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
              ))}
              {requests.length === 0 ? (
                <tr><td colSpan={10} className={styles.empty}>No requests match the current filters.</td></tr>
              ) : null}
            </tbody>
          </table>
          <footer className={styles.tableFooter}>
            <span>Showing {requests.length} of {activity.requests.length}</span>
            <span>Live buffer · 100 visible</span>
          </footer>
        </div>
        {selected && inspectorOpen ? <RequestInspector request={selected} onClose={() => setInspectorOpen(false)} /> : null}
      </div>
    </section>
  )
}

function RequestInspector({ request, onClose }: { request: ActivityRequest; onClose: () => void }) {
  return (
    <aside className={styles.inspector} aria-label="Request inspector">
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
      {request.errorCode ? (
        <div className={styles.safeError}>
          <span>Safe error code</span>
          <strong>{request.errorCode}</strong>
        </div>
      ) : null}
    </aside>
  )
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
