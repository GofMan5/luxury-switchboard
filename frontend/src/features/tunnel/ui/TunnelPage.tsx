import { useState, type FormEvent } from 'react'
import { Check, Copy, KeyRound, Play, RotateCw, Square } from 'lucide-react'
import { Button } from '../../../shared/ui/Button'
import { StatusDot } from '../../../shared/ui/StatusDot'
import type { PrivacyReport } from '../domain/privacy'
import type { TunnelConfig, TunnelSnapshot } from '../domain/tunnel'
import { PrivacyReportPanel } from './PrivacyReportPanel'
import { useTunnel } from './useTunnel'
import { tunnelLimitsAreValid } from './tunnel-form'
import styles from './TunnelPage.module.css'

export default function TunnelPage() {
  const { model, state } = useTunnel()
  const snapshot = state.snapshot
  if (!snapshot) return <section><header className="page-header"><div><h1>Tunnel</h1><p>Loading secure gateway…</p></div></header></section>
  return <TunnelForm key={`${snapshot.address}:${snapshot.port}:${snapshot.rpmPerIp}:${snapshot.contextLimitKiB}:${snapshot.brandResponse}`} snapshot={snapshot} pending={state.pending} error={state.error} onSave={(config) => model.configure(config)} onStart={() => model.start()} onStop={() => model.stop()} onReveal={() => model.reveal()} onRotate={() => model.rotate()} onPrivacyTest={() => model.privacyTest()} />
}

function TunnelForm({ snapshot, pending, error, onSave, onStart, onStop, onReveal, onRotate, onPrivacyTest }: {
  snapshot: TunnelSnapshot
  pending: boolean
  error: string
  onSave: (config: TunnelConfig) => Promise<boolean>
  onStart: () => Promise<boolean>
  onStop: () => Promise<boolean>
  onReveal: () => Promise<string>
  onRotate: () => Promise<string>
  onPrivacyTest: () => Promise<PrivacyReport>
}) {
  const [port, setPort] = useState(String(snapshot.port))
  const [rpm, setRPM] = useState(String(snapshot.rpmPerIp))
  const [contextMiB, setContextMiB] = useState(String(snapshot.contextLimitKiB / 1024))
  const [brand, setBrand] = useState(snapshot.brandResponse)
  const [notice, setNotice] = useState('')
  const [privacyReport, setPrivacyReport] = useState<PrivacyReport | null>(null)
  const [privacyPending, setPrivacyPending] = useState(false)
  const [privacyError, setPrivacyError] = useState('')
  const retryStop = snapshot.state === 'error' && Boolean(snapshot.address)
  const running = snapshot.state === 'online' || snapshot.state === 'starting' || snapshot.state === 'installing' || retryStop
  const parsedPort = Number(port)
  const parsedRPM = Number(rpm)
  const parsedContext = Number(contextMiB)
  const valid = tunnelLimitsAreValid(port, rpm, contextMiB)
  const dirty = parsedPort !== snapshot.port || parsedRPM !== snapshot.rpmPerIp || Math.round(parsedContext * 1024) !== snapshot.contextLimitKiB || brand !== snapshot.brandResponse
  const config = { port: parsedPort, rpmPerIp: parsedRPM, contextLimitKiB: Math.round(parsedContext * 1024), brandResponse: brand }
  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (!valid) { setNotice('Check the tunnel settings before saving'); return }
    void onSave(config)
  }
  const copy = async (value: string, label: string) => {
    if (!value) return
    try {
      await navigator.clipboard.writeText(value)
      setNotice(`${label} copied`)
    } catch {
      setNotice('Clipboard is unavailable')
    }
  }
  const start = async () => {
    if (!valid) { setNotice('Check the tunnel settings before starting'); return }
    if (dirty && !(await onSave(config))) return
    await onStart()
  }
  const testPrivacy = async () => {
    if (privacyPending || !snapshot.address) return
    setPrivacyPending(true)
    setPrivacyError('')
    setPrivacyReport(null)
    try {
      setPrivacyReport(await onPrivacyTest())
    } catch {
      setPrivacyError('Privacy test could not reach the published tunnel')
    } finally {
      setPrivacyPending(false)
    }
  }
  const stateLabel = snapshot.state === 'online' ? 'Online' : snapshot.state === 'starting' ? 'Connecting' : snapshot.state === 'installing' ? 'Installing the connector' : snapshot.state === 'error' ? 'Unavailable' : 'Stopped'
  const stateTone = snapshot.state === 'online' ? 'healthy' : snapshot.state === 'error' ? 'error' : snapshot.state === 'starting' || snapshot.state === 'installing' ? 'active' : 'stopped'
  const working = snapshot.state === 'starting' || snapshot.state === 'installing'

  return <form className={styles.page} onSubmit={submit}>
    <header className="page-header"><div><h1>Tunnel</h1><p>Fail-closed public model gateway over a quick tunnel — no host, no account, no SSH</p></div><div className={styles.actions}>{running ? <Button type="button" variant="danger" disabled={pending} onClick={() => void onStop()}><Square size={13} fill="currentColor" />{retryStop ? 'Retry stop' : 'Stop'}</Button> : <Button type="button" variant="primary" disabled={pending || !valid} onClick={() => void start()}><Play size={15} />{dirty ? 'Save & start' : 'Start'}</Button>}</div></header>
    {error ? <div className={styles.error} role="alert">{error}</div> : null}{notice ? <div className={styles.notice} aria-live="polite">{notice}</div> : null}
    <div className={`page-body ${styles.content}`}>
      <section className={styles.status} data-working={working || undefined}>
        <div className={styles.statusMain}>
          <StatusDot state={stateTone} />
          <div>
            <h2>{stateLabel}</h2>
            <p>{snapshot.state === 'installing' ? 'First start downloads the tunnel connector once — verified against its pinned checksum.' : snapshot.address || 'Gateway is closed to public traffic.'}</p>
            {snapshot.error && snapshot.state !== 'error' ? <p className={styles.statusStep}>{snapshot.error}</p> : null}
          </div>
        </div>
        {working ? <div className={styles.progress} role="progressbar" aria-label={stateLabel} /> : null}
        <div className={styles.statusActions}>
          <Button type="button" disabled={snapshot.state !== 'online'} onClick={() => void copy(snapshot.address, 'Tunnel URL')}><Copy size={14} />Copy URL</Button>
          <CopyKey onReveal={onReveal} />
          <Button type="button" disabled={running || pending} onClick={async () => { const token = await onRotate(); if (token) void copy(token, 'New access key') }}><RotateCw size={14} />Rotate</Button>
        </div>
      </section>
      {snapshot.state === 'online' ? (
        <p className={styles.addressNote}>A quick tunnel re-rolls its public address on every start — share the current one. The access key is stable across restarts.</p>
      ) : null}
      <section className={styles.settings}>
        <header><h2>Public limits</h2><p>Changes apply while the gateway is stopped.</p></header>
        <div className={styles.fields}>
          <label><span>Local gateway port</span><input type="number" min="1" max="65535" step="1" required value={port} disabled={running} onChange={(event) => setPort(event.currentTarget.value)} /><small>The sanitized gateway listens on loopback; the tunnel connector forwards it.</small></label>
          <label><span>Requests per IP / min</span><input type="number" min="0" max="1000000" step="1" required value={rpm} disabled={running} onChange={(event) => setRPM(event.currentTarget.value)} /><small>0 means unlimited.</small></label>
          <label><span>Context limit</span><span className={styles.suffixed}><input type="number" min="0" max="2048" step="0.001" required value={contextMiB} disabled={running} onChange={(event) => setContextMiB(event.currentTarget.value)} /><small>MiB</small></span></label>
          <label className={styles.brand}><span>Public response prefix</span><input value={brand} maxLength={500} disabled={running} onChange={(event) => setBrand(event.currentTarget.value)} /><small>Prepended once to each public text answer without changing the model prompt. Provider data remains structurally redacted.</small></label>
        </div>
        <footer><Button type="submit" variant="primary" disabled={running || pending || !dirty || !valid}>{pending ? 'Saving…' : 'Save tunnel settings'}</Button></footer>
      </section>
      <PrivacyReportPanel report={privacyReport} pending={privacyPending} error={privacyError} disabled={pending || !snapshot.address} onRun={() => void testPrivacy()} />
    </div>
  </form>
}

function CopyKey({ onReveal }: { onReveal: () => Promise<string> }) {
  const [copied, setCopied] = useState(false)
  return (
    <Button type="button" onClick={async () => {
      const token = await onReveal()
      if (!token) return
      try {
        await navigator.clipboard.writeText(token)
        setCopied(true)
        window.setTimeout(() => setCopied(false), 2_000)
      } catch { /* clipboard is a courtesy */ }
    }}>
      {copied ? <Check size={14} /> : <KeyRound size={14} />}{copied ? 'Copied' : 'Copy key'}
    </Button>
  )
}
