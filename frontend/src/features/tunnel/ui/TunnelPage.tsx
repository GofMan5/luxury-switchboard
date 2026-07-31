import { useState, type FormEvent } from 'react'
import { Copy, KeyRound, Play, RotateCw, ShieldCheck, Square } from 'lucide-react'
import { Button } from '../../../shared/ui/Button'
import { StatusDot } from '../../../shared/ui/StatusDot'
import type { TunnelConfig, TunnelSnapshot } from '../domain/tunnel'
import { useTunnel } from './useTunnel'
import styles from './TunnelPage.module.css'

export default function TunnelPage() {
  const { model, state } = useTunnel()
  const snapshot = state.snapshot
  if (!snapshot) return <section><header className="page-header"><div><h1>Tunnel</h1><p>Loading secure gateway…</p></div></header></section>
  return <TunnelForm key={`${snapshot.port}:${snapshot.rpmPerIp}:${snapshot.contextLimitKiB}:${snapshot.brandResponse}:${snapshot.publisherProfile}`} snapshot={snapshot} pending={state.pending} error={state.error} onSave={(config) => model.configure(config)} onStart={() => model.start()} onStop={() => model.stop()} onReveal={() => model.reveal()} onRotate={() => model.rotate()} />
}

function TunnelForm({ snapshot, pending, error, onSave, onStart, onStop, onReveal, onRotate }: {
  snapshot: TunnelSnapshot
  pending: boolean
  error: string
  onSave: (config: TunnelConfig) => Promise<boolean>
  onStart: () => Promise<boolean>
  onStop: () => Promise<boolean>
  onReveal: () => Promise<string>
  onRotate: () => Promise<string>
}) {
  const [port, setPort] = useState(String(snapshot.port))
  const [rpm, setRPM] = useState(String(snapshot.rpmPerIp))
  const [contextMiB, setContextMiB] = useState(String(snapshot.contextLimitKiB / 1024))
  const [brand, setBrand] = useState(snapshot.brandResponse)
  const [publisherProfile, setPublisherProfile] = useState(snapshot.publisherProfile)
  const [notice, setNotice] = useState('')
  const running = snapshot.state === 'online' || snapshot.state === 'starting' || snapshot.state === 'paused'
  const dirty = Number(port) !== snapshot.port || Number(rpm) !== snapshot.rpmPerIp || Math.round(Number(contextMiB) * 1024) !== snapshot.contextLimitKiB || brand !== snapshot.brandResponse || publisherProfile.trim() !== snapshot.publisherProfile
  const submit = (event: FormEvent) => {
    event.preventDefault()
    void onSave({ port: Number(port), rpmPerIp: Number(rpm), contextLimitKiB: Math.round(Number(contextMiB) * 1024), brandResponse: brand, publisherProfile: publisherProfile.trim() })
  }
  const copy = async (value: string, label: string) => {
    if (!value) return
    await navigator.clipboard.writeText(value)
    setNotice(`${label} copied`)
  }

  return <form className={styles.page} onSubmit={submit}>
    <header className="page-header"><div><h1>Tunnel</h1><p>Fail-closed public model gateway</p></div><div className={styles.actions}>{running ? <Button type="button" variant="danger" disabled={pending} onClick={() => void onStop()}><Square size={13} fill="currentColor" />Stop</Button> : <Button type="button" variant="primary" disabled={pending} onClick={() => void onStart()}><Play size={15} />Start</Button>}</div></header>
    {error ? <div className={styles.error}>{error}</div> : null}{notice ? <div className={styles.notice}>{notice}</div> : null}
    <div className={styles.content}>
      <section className={styles.status}><div className={styles.statusMain}><StatusDot state={snapshot.state === 'online' ? 'healthy' : snapshot.state === 'error' ? 'error' : snapshot.state === 'starting' ? 'active' : snapshot.state === 'paused' ? 'degraded' : 'stopped'} /><div><h2>{snapshot.state === 'online' ? 'Online' : snapshot.state === 'starting' ? 'Connecting' : snapshot.state === 'paused' ? 'Paused' : snapshot.state === 'error' ? 'Unavailable' : 'Stopped'}</h2><p>{snapshot.address || 'Gateway is closed to public traffic.'}</p></div></div><div className={styles.statusActions}><Button type="button" disabled={snapshot.state !== 'online'} onClick={() => void copy(snapshot.address, 'Tunnel URL')}><Copy size={14} />Copy URL</Button><Button type="button" onClick={async () => void copy(await onReveal(), 'Access key')}><KeyRound size={14} />Copy key</Button><Button type="button" disabled={running || pending} onClick={async () => { const token = await onRotate(); if (token) void copy(token, 'New access key') }}><RotateCw size={14} />Rotate</Button></div></section>
      <section className={styles.settings}><header><h2>Public limits and identity</h2><p>Changes are allowed only while the gateway is stopped.</p></header><div className={styles.fields}><label><span>Local gateway port</span><input type="number" min="1" max="65535" value={port} disabled={running} onChange={(event) => setPort(event.currentTarget.value)} /></label><label><span>Requests per IP / min</span><input type="number" min="0" value={rpm} disabled={running} onChange={(event) => setRPM(event.currentTarget.value)} /><small>0 means unlimited.</small></label><label><span>Context limit</span><span className={styles.suffixed}><input type="number" min="0" value={contextMiB} disabled={running} onChange={(event) => setContextMiB(event.currentTarget.value)} /><small>MiB</small></span></label><label className={styles.brand}><span>Publisher profile</span><input value={publisherProfile} placeholder="v1.20000.48-character-slug" disabled={running} onChange={(event) => setPublisherProfile(event.currentTarget.value)} /><small>Leave empty for loopback-only mode. The publisher key remains a separate local file.</small></label><label className={styles.brand}><span>Provider identity response</span><input value={brand} maxLength={500} disabled={running} onChange={(event) => setBrand(event.currentTarget.value)} /><small>Provider data is still structurally redacted from every public response.</small></label></div><footer><Button type="submit" variant="primary" disabled={running || pending || !dirty}>{pending ? 'Saving…' : 'Save tunnel settings'}</Button></footer></section>
      <section className={styles.privacy}><ShieldCheck size={20} /><div><h2>Provider privacy is fail-closed</h2><p><code>/v1/models</code> contains only selected public aliases. Responses remove provider/upstream/owned_by fields, rewrite model IDs, canonicalize SSE and replace configured provider/key/proxy markers before public commit.</p></div></section>
    </div>
  </form>
}
