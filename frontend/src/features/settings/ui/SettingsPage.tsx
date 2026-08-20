import { useState, type ChangeEvent, type FormEvent, type ReactNode } from 'react'
import { Database, Gauge, History, RotateCw, Save, ShieldCheck } from 'lucide-react'
import { restartApp } from '../../../platform/lifecycle/restart-app'
import { Button } from '../../../shared/ui/Button'
import type { Settings } from '../domain/settings'
import { useSettings } from './useSettings'
import styles from './SettingsPage.module.css'

export default function SettingsPage() {
  const { model, state } = useSettings()
  if (!state.settings) {
    return <section className={styles.page}><header className="page-header"><div><h1>Settings</h1><p>Loading validated runtime configuration…</p></div></header></section>
  }
  return <SettingsForm key={JSON.stringify(state.settings)} initial={state.settings} pending={state.pending} restartRequired={state.restartRequired} error={state.error} onSave={(value) => model.save(value)} onRestart={restartApp} />
}

export function SettingsForm({ initial, pending, restartRequired, error, onSave, onRestart }: { initial: Settings; pending: boolean; restartRequired: boolean; error: string; onSave: (value: Settings) => Promise<boolean>; onRestart: () => Promise<void> }) {
  const [settings, setSettings] = useState(initial)
  const [restarting, setRestarting] = useState(false)
  const [restartError, setRestartError] = useState('')
  const dirty = JSON.stringify(settings) !== JSON.stringify(initial)
  const number = (field: keyof Settings) => (event: ChangeEvent<HTMLInputElement>) => {
    const value = Number(event.currentTarget.value)
    setSettings((current) => ({ ...current, [field]: value }))
  }
  const submit = (event: FormEvent) => { event.preventDefault(); void onSave(settings) }
  const restart = async () => {
    setRestarting(true)
    setRestartError('')
    try {
      await onRestart()
    } catch {
      setRestarting(false)
      setRestartError('Luxury Switchboard could not restart. Close and reopen it to apply the saved settings.')
    }
  }

  return (
    <form className={styles.page} onSubmit={submit}>
      <header className="page-header">
        <div><h1>Settings</h1><p>Safe defaults for relay, reliability and storage</p></div>
        <Button type="submit" variant="primary" disabled={!dirty || pending}><Save size={15} />{pending ? 'Saving…' : 'Save settings'}</Button>
      </header>
      {restartRequired ? <div className={styles.restart} aria-live="polite" aria-busy={restarting}><RotateCw size={17} /><div><strong>Restart Luxury Switchboard to apply runtime changes</strong><span>Saved values are already encrypted; active requests are cancelled cleanly during restart.</span></div><Button type="button" disabled={restarting} onClick={() => void restart()}>{restarting ? 'Restarting…' : 'Restart now'}</Button></div> : null}
      {error || restartError ? <div className={styles.error} role="alert">{error || restartError}</div> : null}

      <div className={styles.content}>
        <SettingsSection icon={<Gauge />} title="Local relay" description="Listener and bounded request admission.">
          <NumberField label="Listener port" value={settings.listenerPort} min={1} max={65535} onChange={number('listenerPort')} note="Loopback-only; remote binding is never allowed." />
          <NumberField label="Maximum request" value={settings.maxRequestMiB} min={1} max={256} suffix="MiB" onChange={number('maxRequestMiB')} />
          <NumberField label="Maximum queued" value={settings.maxQueued} min={100} max={100000} onChange={number('maxQueued')} note="Wait time is unlimited; memory is not." />
        </SettingsSection>

        <SettingsSection icon={<ShieldCheck />} title="Reliability" description="Timeout and retry behavior shared by all provider profiles.">
          <NumberField label="Response headers" value={settings.headerTimeoutSeconds} min={5} max={300} suffix="sec" onChange={number('headerTimeoutSeconds')} />
          <NumberField label="Stream idle" value={settings.streamIdleSeconds} min={15} max={900} suffix="sec" onChange={number('streamIdleSeconds')} />
          <NumberField label="Retry base" value={settings.retryBaseMilliseconds} min={50} max={10000} suffix="ms" onChange={number('retryBaseMilliseconds')} />
          <NumberField label="Retry maximum" value={settings.retryMaxSeconds} min={1} max={120} suffix="sec" onChange={number('retryMaxSeconds')} />
          <NumberField label="Permanent attempts" value={settings.permanentAttempts} min={1} max={3} onChange={number('permanentAttempts')} note="429, transport and 5xx remain cancellable seamless retries." />
        </SettingsSection>

        <SettingsSection icon={<Database />} title="Live data" description="Bounded in-memory activity and persistent history policy.">
          <NumberField label="Live activity rows" value={settings.activityCapacity} min={100} max={20000} onChange={number('activityCapacity')} />
          <NumberField label="History retention" value={settings.historyRetentionDays} min={1} max={365} suffix="days" onChange={number('historyRetentionDays')} />
          {/* The public sidecar has no tunnel, so it keeps no tunnel log to retain.
              The literal is tested here rather than a runtime flag so the field
              leaves the public bundle entirely. */}
          {__OWNER_EDITION__ ? <NumberField label="Tunnel log retention" value={settings.tunnelRetentionHours} min={24} max={720} suffix="hours" onChange={number('tunnelRetentionHours')} /> : null}
          <NumberField label="Guardrail findings" value={settings.guardrailFindings} min={50} max={5000} onChange={number('guardrailFindings')} note="Inspection mode is chosen on the Guardrails page." />
        </SettingsSection>

        <section className={styles.invariant}><History size={18} /><div><strong>Security boundaries are fixed</strong><p>Loopback binding, remote HTTPS, secret redaction, body/frame caps{__OWNER_EDITION__ ? ' and fail-closed tunnel sanitization' : ''} cannot be disabled from Settings.</p></div></section>
      </div>
    </form>
  )
}

function SettingsSection({ icon, title, description, children }: { icon: ReactNode; title: string; description: string; children: ReactNode }) {
  return <section className={styles.section}><header><span>{icon}</span><div><h2>{title}</h2><p>{description}</p></div></header><div className={styles.fields}>{children}</div></section>
}

function NumberField({ label, value, min, max, suffix, note, onChange }: { label: string; value: number; min: number; max: number; suffix?: string; note?: string; onChange: (event: ChangeEvent<HTMLInputElement>) => void }) {
  return <label className={styles.field}><span>{label}</span><span className={styles.inputWrap}><input type="number" value={value} min={min} max={max} step="1" onChange={onChange} />{suffix ? <small>{suffix}</small> : null}</span>{note ? <em>{note}</em> : null}</label>
}
