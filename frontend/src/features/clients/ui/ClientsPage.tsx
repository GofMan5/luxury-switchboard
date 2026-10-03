import { useEffect, useState } from 'react'
import { Ban, NotebookPen, RefreshCw, ShieldCheck, UsersRound, X } from 'lucide-react'
import { formatBytes, formatClock, formatDuration } from '../../../shared/format/metrics'
import { Button } from '../../../shared/ui/Button'
import { Metric, MetricStrip } from '../../../shared/ui/chrome'
import { StatusDot } from '../../../shared/ui/StatusDot'
import { useModalFocus } from '../../../shared/ui/useModalFocus'
import { MAX_CLIENT_NOTE, type TunnelClient, type TunnelClientEvent, type TunnelClientProfile } from '../domain/client'
import { useClients } from './useClients'
import styles from './ClientsPage.module.css'

export default function ClientsPage() {
  const { model, state } = useClients()
  const selected = state.clients.find((client) => client.ip === state.selectedIp)
  const metrics = state.clients.reduce((total, client) => {
    if (client.active > 0) total.active++
    total.queued += client.queued
    total.rpm += client.actualRpm
    if (client.banned) total.banned++
    return total
  }, { active: 0, queued: 0, rpm: 0, banned: 0 })

  return (
    <section className={styles.page}>
      <header className="page-header">
        <div><h1>Tunnel Clients</h1><p>Live per-IP load, owner notes and sanitized request history</p></div>
        <Button disabled={state.phase === 'loading'} onClick={() => void model.refresh()}>
          <RefreshCw size={15} aria-hidden="true" />Refresh
        </Button>
      </header>

      {state.error ? <div className={styles.error} role="alert">{state.error}</div> : null}

      <div className="page-body">
        <MetricStrip>
          <Metric label="Connected clients" value={state.clients.length.toLocaleString()} />
          <Metric label="Active now" value={metrics.active.toLocaleString()} tone={metrics.active > 0 ? 'success' : undefined} />
          <Metric label="Queued" value={metrics.queued.toLocaleString()} tone={metrics.queued > 0 ? 'warning' : undefined} />
          <Metric label="Banned" value={metrics.banned.toLocaleString()} tone={metrics.banned > 0 ? 'danger' : undefined} />
          <Metric label="Current RPM" value={metrics.rpm.toLocaleString()} />
        </MetricStrip>

        <section className={styles.clients}>
        <header>
          <div><UsersRound size={17} aria-hidden="true" /><h2>Clients</h2></div>
          <span>Bans apply to new requests. Notes stay on this machine.</span>
        </header>
        <div className={styles.tableWrap}>
          <table>
            <thead><tr><th>State</th><th>Client IP</th><th>Note</th><th>RPM</th><th>Active</th><th>Queued</th><th>Requests</th><th>Last seen</th><th>Actions</th></tr></thead>
            <tbody>
              {state.clients.map((client) => (
                <ClientRow
                  key={client.ip}
                  client={client}
                  pending={state.pendingIp === client.ip}
                  onOpen={() => void model.select(client.ip)}
                  onBan={() => void model.saveProfile({ ip: client.ip, banned: !client.banned, note: client.note })}
                />
              ))}
              {state.phase !== 'loading' && state.clients.length === 0 ? <tr><td colSpan={9} className={styles.empty}>No tunnel clients yet.</td></tr> : null}
            </tbody>
          </table>
        </div>
        </section>
      </div>

      {state.selectedIp ? (
        <ClientDialog
          client={selected}
          ip={state.selectedIp}
          events={state.events}
          pending={state.pendingIp === state.selectedIp}
          onClose={() => model.close()}
          onSave={(profile) => model.saveProfile(profile)}
        />
      ) : null}
    </section>
  )
}

function clientState(client: Pick<TunnelClient, 'active' | 'queued' | 'banned'>) {
  if (client.banned) return { label: 'Banned', tone: 'failed' as const }
  if (client.active > 0) return { label: 'Active', tone: 'active' as const }
  if (client.queued > 0) return { label: 'Queued', tone: 'retrying' as const }
  return { label: 'Idle', tone: 'stopped' as const }
}

function ClientRow({ client, pending, onOpen, onBan }: { client: TunnelClient; pending: boolean; onOpen: () => void; onBan: () => void }) {
  const view = clientState(client)
  return (
    <tr
      data-banned={client.banned || undefined}
      tabIndex={0}
      onDoubleClick={onOpen}
      onKeyDown={(event) => {
        if (event.key !== 'Enter') return
        event.preventDefault()
        onOpen()
      }}
    >
      <td><span className={styles.state}><StatusDot state={view.tone} />{view.label}</span></td>
      {/* The address is the row's identity, and it does not fit at the minimum
          window width: a compressed IPv6 needs about twice this column. The note
          below already carries its full text in a title; without one here two
          clients from the same /64 render as the same ellipsis. */}
      <td className={styles.mono} title={client.ip}>{client.ip}</td>
      <td className={styles.note} title={client.note || undefined}>{client.note || <span className={styles.muted}>—</span>}</td>
      <td>{client.actualRpm}</td>
      <td>{client.active}</td>
      <td>{client.queued}</td>
      <td>{client.count.toLocaleString()}</td>
      <td>{client.lastSeen ? formatClock(client.lastSeen) : '—'}</td>
      <td>
        <div className={styles.actions}>
          <button type="button" aria-label={`Open logs and notes for ${client.ip}`} onClick={onOpen}><NotebookPen size={15} aria-hidden="true" /></button>
          <button
            type="button"
            data-danger={!client.banned || undefined}
            disabled={pending}
            aria-label={client.banned ? `Unban ${client.ip}` : `Ban ${client.ip}`}
            onClick={onBan}
          >
            {client.banned ? <ShieldCheck size={15} aria-hidden="true" /> : <Ban size={15} aria-hidden="true" />}
          </button>
        </div>
      </td>
    </tr>
  )
}

function ClientDialog({ client, ip, events, pending, onClose, onSave }: {
  client?: TunnelClient
  ip: string
  events: readonly TunnelClientEvent[]
  pending: boolean
  onClose: () => void
  onSave: (profile: TunnelClientProfile) => Promise<boolean>
}) {
  const dialogRef = useModalFocus<HTMLElement>(onClose)
  const banned = client?.banned ?? false
  const storedNote = client?.note ?? ''
  const [note, setNote] = useState(storedNote)
  useEffect(() => { setNote(storedNote) }, [storedNote])
  const view = clientState({ active: client?.active ?? 0, queued: client?.queued ?? 0, banned })
  const noteDirty = note.trim() !== storedNote

  return (
    <div className={styles.backdrop} role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose() }}>
      <section ref={dialogRef} className={styles.dialog} role="dialog" aria-modal="true" aria-labelledby="client-dialog-title">
        <header>
          <div>
            <span className={styles.state}><StatusDot state={view.tone} />{view.label === 'Active' ? 'Active now' : view.label}</span>
            <h2 id="client-dialog-title">{ip}</h2>
          </div>
          <div className={styles.dialogActions}>
            <Button
              type="button"
              variant={banned ? 'primary' : 'danger'}
              disabled={pending}
              onClick={() => void onSave({ ip, banned: !banned, note })}
            >
              {banned ? <ShieldCheck size={14} aria-hidden="true" /> : <Ban size={14} aria-hidden="true" />}
              {banned ? 'Unban client' : 'Ban client'}
            </Button>
            <button type="button" className={styles.close} aria-label="Close client logs" onClick={onClose}><X size={18} aria-hidden="true" /></button>
          </div>
        </header>
        <div className={styles.clientSummary}>
          <span><small>RPM</small><strong>{client?.actualRpm ?? 0}</strong></span>
          <span><small>Active</small><strong>{client?.active ?? 0}</strong></span>
          <span><small>Queued</small><strong>{client?.queued ?? 0}</strong></span>
          <span><small>Total</small><strong>{(client?.count ?? 0).toLocaleString()}</strong></span>
          <span><small>Refused</small><strong>{(client?.refused ?? 0).toLocaleString()}</strong></span>
        </div>
        <form
          className={styles.noteForm}
          onSubmit={(event) => {
            event.preventDefault()
            void onSave({ ip, banned, note })
          }}
        >
          <label>
            <span>Owner note</span>
            <input
              value={note}
              maxLength={MAX_CLIENT_NOTE}
              placeholder="Who this client is, why it is trusted or blocked"
              onChange={(event) => setNote(event.currentTarget.value)}
            />
          </label>
          <Button type="submit" disabled={pending || !noteDirty}>{pending ? 'Saving…' : 'Save note'}</Button>
        </form>
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
