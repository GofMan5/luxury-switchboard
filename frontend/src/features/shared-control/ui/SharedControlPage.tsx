import { Pause, Play, RefreshCw, Square } from 'lucide-react'
import { Button } from '../../../shared/ui/Button'
import { StatusDot } from '../../../shared/ui/StatusDot'
import type { SharedTunnel } from '../domain/snapshot'
import { useShared } from './useShared'
import styles from './SharedControlPage.module.css'

export default function SharedControlPage() {
  const { model, state } = useShared()
  const snapshot = state.snapshot
  return <section className={styles.page}>
    <header className="page-header"><div><h1>Shared Control</h1><p>Revision-safe control of every published tunnel</p></div><Button disabled={state.refreshing} onClick={() => void model.refresh()}><RefreshCw size={15} />{state.refreshing ? 'Syncing…' : 'Refresh'}</Button></header>
    {state.error ? <div className={styles.error} role="alert">{state.error}</div> : null}
    <div className={styles.summary}><span><StatusDot state={snapshot.available ? snapshot.stale ? 'degraded' : 'healthy' : 'error'} />{snapshot.available ? snapshot.stale ? 'Last known state' : 'Synchronized' : 'Unavailable'}</span><small>Revision {snapshot.revision}</small></div>
    <div className={styles.grid}>
      {snapshot.tunnels.map((tunnel) => <TunnelCard key={tunnel.position} tunnel={tunnel} pending={state.pendingPosition === tunnel.position} locked={state.pendingPosition !== null} onAction={(action) => model.control(tunnel.position, action)} />)}
      {snapshot.available && snapshot.tunnels.length === 0 ? <div className={styles.empty}>No shared tunnels are registered.</div> : null}
    </div>
  </section>
}

function TunnelCard({ tunnel, pending, locked, onAction }: { tunnel: SharedTunnel; pending: boolean; locked: boolean; onAction: (action: 'pause' | 'resume' | 'stop') => Promise<boolean> }) {
  const own = tunnel.name === 'Ваш коннект'
  return <article className={styles.card} data-own={own}>
    <header><div><span className={styles.state}><StatusDot state={tunnel.state === 'running' ? 'healthy' : tunnel.state === 'paused' ? 'degraded' : 'stopped'} />{tunnel.state}</span><h2>{tunnel.name}</h2></div>{own ? <strong>Your connection</strong> : null}</header>
    <footer>
      {tunnel.state === 'running' ? <Button disabled={locked} onClick={() => void onAction('pause')}><Pause size={14} />Pause</Button> : <Button disabled={locked} onClick={() => void onAction('resume')}><Play size={14} />Resume</Button>}
      <Button variant="danger" disabled={locked || tunnel.state === 'stopped'} onClick={() => void onAction('stop')}><Square size={12} fill="currentColor" />{pending ? 'Applying…' : 'Stop'}</Button>
    </footer>
  </article>
}
