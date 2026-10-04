import { useDeferredValue, useEffect, useMemo, useRef, useState } from 'react'
import { Check, Copy, Pause, Play, Search, X } from 'lucide-react'
import { useAppServices } from '../../../app/services'
import { formatBytes, formatClock, formatDecimal, formatDuration, formatInteger } from '../../../shared/format/metrics'
import { Button } from '../../../shared/ui/Button'
import { Pill, Segmented } from '../../../shared/ui/chrome'
import { StatusDot } from '../../../shared/ui/StatusDot'
import type { ActivityRequest, ActivityState } from '../domain/activity'
import { activityLabels } from './activity-view'
import { useActivity } from './useActivity'
import styles from './ActivityPage.module.css'

type StateFilter = 'all' | ActivityState

const skeletonRows = [0, 1, 2, 3, 4, 5]

const statePillTone: Record<ActivityState, 'info' | 'warning' | 'success' | 'danger' | 'neutral'> = {
  active: 'info',
  retrying: 'warning',
  completed: 'success',
  failed: 'danger',
  cancelled: 'neutral',
}

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
    if (id === selectedID && inspectorOpen) {
      setInspectorOpen(false)
      return
    }
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
          <Search size={15} aria-hidden="true" />
          <span className="sr-only">Search requests</span>
          <input
            type="search"
            value={search}
            placeholder="Search model, route, request…"
            onChange={(event) => setSearch(event.currentTarget.value)}
          />
        </label>
        <Segmented<StateFilter>
          label="Filter state"
          value={stateFilter}
          onChange={setStateFilter}
          options={[
            { id: 'all', label: 'All' },
            { id: 'active', label: 'Live' },
            { id: 'retrying', label: 'Retrying' },
            { id: 'completed', label: 'Done' },
            { id: 'failed', label: 'Error' },
            { id: 'cancelled', label: 'Cancelled' },
          ]}
        />
        <span className={styles.toolbarSpacer} aria-hidden="true" />
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
        <div className={styles.tablePane}>
          {loading ? <p className="sr-only" role="status">Loading live activity…</p> : null}
          <div className={styles.scroll} aria-busy={loading}>
            <table className={styles.table} aria-label="Live requests">
              <thead>
                <tr>
                  <th scope="col">State</th>
                  <th scope="col">Model</th>
                  <th scope="col">Route</th>
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
                  // The highlight means "this row is what the panel shows": a
                  // closed panel marks nothing, even though the id is kept so
                  // the Details button can reopen the same request.
                  const isSelected = request.id === selectedID && inspectorOpen
                  return (
                    <tr
                      key={request.id}
                      data-selected={isSelected}
                      data-row=""
                      aria-selected={isSelected}
                      tabIndex={request.id === selectedID || (selectedID === '' && index === 0) ? 0 : -1}
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
                      <td><Pill tone={statePillTone[request.state]}>{activityLabels[request.state]}</Pill></td>
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
          </div>
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
  // Non-modal side panel: no autofocus, no Tab trap — those would be modal
  // semantics over a workspace that keeps running behind the panel. Escape
  // closes it from anywhere inside.
  const panelRef = useRef<HTMLElement>(null)
  useEffect(() => {
    const panel = panelRef.current
    if (!panel) return
    const keydown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault()
        onClose()
      }
    }
    panel.addEventListener('keydown', keydown)
    return () => panel.removeEventListener('keydown', keydown)
  }, [onClose])
  const [copied, setCopied] = useState(false)
  const copyId = async () => {
    try {
      await navigator.clipboard.writeText(request.id)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 2000)
    } catch { /* the id stays on screen either way */ }
  }
  // Non-modal side panel: live updates continue behind it, so modal semantics would lie.
  return (
    <aside ref={panelRef} className={styles.inspector} role="complementary" aria-label="Request inspector">
      <header>
        <div>
          <h2>Request Inspector</h2>
          <span className={styles.requestID}>
            {request.id}
            <button type="button" className={styles.copyId} onClick={() => void copyId()} aria-label={copied ? 'Request id copied' : 'Copy request id'}>
              {copied ? <Check size={12} aria-hidden="true" /> : <Copy size={12} aria-hidden="true" />}
            </button>
          </span>
        </div>
        <button type="button" className={styles.close} aria-label="Close inspector" onClick={onClose}>
          <X size={17} aria-hidden="true" />
        </button>
      </header>

      <div className={styles.inspectorScroll}>
        <div className={styles.inspectorStatus}>
          <Pill tone={statePillTone[request.state]}>{activityLabels[request.state]}</Pill>
          <span className={styles.inspectorMeta}>{request.method} {request.path}{request.status ? ` · HTTP ${request.status}` : ''}</span>
        </div>

        <section className={styles.inspectorSection} aria-label="Timing">
          <dl className={styles.details}>
            <Detail label="Model" value={request.model || '—'} />
            <Detail label="Route" value={request.providerName || '—'} />
            <Detail label="Latency" value={formatDuration(request.latencyMs)} />
            <Detail label="Queue / retries" value={`${formatDuration(request.queueMs)} · ${request.retries}`} />
            <Detail label="Traffic" value={`${formatBytes(request.bytesIn)} in · ${formatBytes(request.bytesOut)} out`} />
            <Detail label="Generation" value={request.tokensPerSecond > 0 ? `${formatDecimal(request.tokensPerSecond)} tok/s` : '—'} />
          </dl>
        </section>

        <section className={styles.inspectorSection} aria-label="Tokens">
          <div className={styles.tokenGrid}>
            <TokenStat label="Input" value={request.inputTokens} />
            <TokenStat label="Output" value={request.outputTokens} />
            <TokenStat label="Cached" value={request.cachedTokens} />
            <TokenStat label="Reasoning" value={request.reasoningTokens} />
            <TokenStat label="Processed" value={request.totalTokens} />
            <TokenStat label="Context" value={request.contextTokens} />
          </div>
        </section>

        <section className={styles.inspectorSection} aria-label="Status timeline">
          <h3 className={styles.sectionTitle}>Timeline</h3>
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
      </div>
    </aside>
  )
}

function TokenStat({ label, value }: { label: string; value: number }) {
  return (
    <div className={styles.tokenStat}>
      <span>{label}</span>
      <strong>{formatInteger(value)}</strong>
    </div>
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
