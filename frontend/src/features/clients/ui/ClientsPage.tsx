import { RefreshCw, UsersRound, X } from 'lucide-react'
import { formatBytes, formatClock, formatDuration } from '../../../shared/format/metrics'
import { Button } from '../../../shared/ui/Button'
import { StatusDot } from '../../../shared/ui/StatusDot'
import type { TunnelClient, TunnelClientEvent } from '../domain/client'
import { useClients } from './useClients'
import styles from './ClientsPage.module.css'

export default function ClientsPage() {
  const { model, state } = useClients()
  const selected = state.clients.find((client) => client.ip === state.selectedIp)
  const active = state.clients.filter((client) => client.active > 0).length
  const queued = state.clients.reduce((sum, client) => sum + client.queued, 0)

  return (
    <section className={styles.page}>
      <header className="page-header">
        <div><h1>Tunnel Clients</h1><p>Live per-IP load and sanitized request history</p></div>
        <Button disabled={state.phase === 'loading'} onClick={() => void model.refresh()}>
          <RefreshCw size={15} aria-hidden="true" />Refresh
        </Button>
      </header>

      {state.error ? <div className={styles.error}>{state.error}</div> : null}

      <div className={styles.metrics}>
        <Metric label="Connected clients" value={state.clients.length} />
        <Metric label="Active now" value={active} tone="active" />
        <Metric label="Queued" value={queued} tone={queued > 0 ? 'queued' : undefined} />
        <Metric label="Current RPM" value={state.clients.reduce((sum, client) => sum + client.actualRpm, 0)} />
      </div>

      <section className={styles.clients}>
        <header>
          <div><UsersRound size={17} aria-hidden="true" /><h2>Clients</h2></div>
          <span>Double-click a row for live logs</span>
        </header>
        <div className={styles.tableWrap}>
          <table>
            <thead><tr><th>State</th><th>Client IP</th><th>RPM</th><th>Active</th><th>Queued</th><th>Requests</th><th>Last seen</th></tr></thead>
            <tbody>
              {state.clients.map((client) => (
                <tr
                  key={client.ip}
                  tabIndex={0}
                  onDoubleClick={() => void model.select(client.ip)}
                  onKeyDown={(event) => {
                    if (event.key !== 'Enter') return
                    event.preventDefault()
                    void model.select(client.ip)
                  }}
                >
                  <td><span className={styles.state}><StatusDot state={client.active > 0 ? 'active' : 'stopped'} />{client.active > 0 ? 'Active' : 'Idle'}</span></td>
                  <td className={styles.mono}>{client.ip}</td>
                  <td>{client.actualRpm}</td>
                  <td>{client.active}</td>
                  <td>{client.queued}</td>
                  <td>{client.count.toLocaleString()}</td>
                  <td>{formatClock(client.lastSeen)}</td>
                </tr>
              ))}
              {state.phase !== 'loading' && state.clients.length === 0 ? <tr><td colSpan={7} className={styles.empty}>No tunnel clients yet.</td></tr> : null}
            </tbody>
          </table>
        </div>
      </section>

      {state.selectedIp ? <ClientDialog client={selected} ip={state.selectedIp} events={state.events} onClose={() => model.close()} /> : null}
    </section>
  )
}

function Metric({ label, value, tone }: { label: string; value: number; tone?: 'active' | 'queued' }) {
  return <div className={styles.metric} data-tone={tone}><span>{label}</span><strong>{value.toLocaleString()}</strong></div>
}

function ClientDialog({ client, ip, events, onClose }: { client?: TunnelClient; ip: string; events: readonly TunnelClientEvent[]; onClose: () => void }) {
  return (
    <div className={styles.backdrop} role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose() }}>
      <section className={styles.dialog} role="dialog" aria-modal="true" aria-labelledby="client-dialog-title">
        <header>
          <div>
            <span className={styles.state}><StatusDot state={client?.active ? 'active' : 'stopped'} />{client?.active ? 'Active now' : 'Idle'}</span>
            <h2 id="client-dialog-title">{ip}</h2>
          </div>
          <button type="button" className={styles.close} aria-label="Close client logs" onClick={onClose}><X size={18} aria-hidden="true" /></button>
        </header>
        <div className={styles.clientSummary}>
          <span><small>RPM</small><strong>{client?.actualRpm ?? 0}</strong></span>
          <span><small>Active</small><strong>{client?.active ?? 0}</strong></span>
          <span><small>Queued</small><strong>{client?.queued ?? 0}</strong></span>
          <span><small>Total</small><strong>{client?.count.toLocaleString() ?? '0'}</strong></span>
        </div>
        <div className={styles.eventTable}>
          <table>
            <thead><tr><th>State</th><th>Model</th><th>Route</th><th>HTTP</th><th>Latency</th><th>Traffic</th><th>Time</th></tr></thead>
            <tbody>
              {events.map((event) => <EventRow key={event.id} event={event} />)}
              {events.length === 0 ? <tr><td colSpan={7} className={styles.empty}>No completed requests for this client.</td></tr> : null}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  )
}

function EventRow({ event }: { event: TunnelClientEvent }) {
  const failed = event.state === 'error' || event.status >= 400
  return (
    <tr title={event.errorCode || undefined}>
      <td><span className={styles.state}><StatusDot state={failed ? 'failed' : 'completed'} />{failed ? event.errorCode || 'Error' : 'Complete'}</span></td>
      <td title={event.model}>{event.model || '—'}</td>
      <td className={styles.mono}>{event.method} {event.path}</td>
      <td>{event.status || '—'}</td>
      <td>{formatDuration(event.latencyMs)}</td>
      <td>{formatBytes(event.bytesIn + event.bytesOut)}</td>
      <td>{formatClock(event.time)}</td>
    </tr>
  )
}
