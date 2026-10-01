import { useContext, useRef, useState, useSyncExternalStore, type ChangeEvent, type FormEvent, type ReactNode } from 'react'
import { ArchiveRestore, Bell, Database, Gauge, GitBranch, History, RotateCw, Save, ShieldCheck } from 'lucide-react'
import { restartApp } from '../../../platform/lifecycle/restart-app'
import { Button } from '../../../shared/ui/Button'
import { ServicesContext } from '../../../app/services'
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
  const toggle = (field: 'notificationsEnabled' | 'providerHealthEnabled' | 'animationsEnabled' | 'failoverEnabled') => () => {
    setSettings((current) => ({ ...current, [field]: !current[field] }))
  }
  const chainMode = (event: ChangeEvent<HTMLSelectElement>) => {
    setSettings((current) => ({ ...current, chainMode: event.currentTarget.value as Settings['chainMode'] }))
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

        <SettingsSection icon={<GitBranch />} title="Failover & routing" description="What happens when a provider in a model route chain answers with a verdict no retry could change.">
          <ToggleField label="Failover chain" checked={settings.failoverEnabled} onChange={toggle('failoverEnabled')} note="On: a dead provider, an exhausted shared quota, a client-level rejection or a missing model moves the request to the next provider of that model's chain. Off: the refusal reaches your client exactly where it happened." />
          <div className={styles.field}>
            <span>Chain mode</span>
            <span className={styles.inputWrap}>
              <select value={settings.chainMode} onChange={chainMode} aria-label="Chain mode">
                <option value="balance">Balance (round-robin)</option>
                <option value="failover">Failover (strict order)</option>
              </select>
            </span>
            <em>Balance spreads requests across every healthy provider of a chain: two providers means twice the daily quota, because the batch quotas resellers run out of are per provider, not per you. Failover sends everything to the head of the chain and only moves on refusal.</em>
          </div>
          <div className={styles.explainer}>
            <strong>How a chain works</strong>
            <p>Publish one model on several providers in Model Routes — for example glm → alpha-relay first, vendor-hub second. A request for glm lands on alpha-relay; when alpha-relay answers with a final verdict (dead keys, a spent quota, a client ban, a model it does not host), the relay degrades alpha-relay for five minutes, rewrites the request to the upstream name of the sibling and sends it there. Your client never sees the failure. After five minutes the chain tries alpha-relay again — in Balance mode it shares the load right away. Configure chains on the Model Routes page; models without a chain keep using the active provider.</p>
          </div>
        </SettingsSection>

        <SettingsSection icon={<Bell />} title="Notifications & appearance" description="What the shell tells you as it happens, and how it moves.">
          <ToggleField label="Notifications" checked={settings.notificationsEnabled} onChange={toggle('notificationsEnabled')} note="Toasts and the unread badge. The feed itself stays recorded either way." />
          <ToggleField label="Provider health probe" checked={settings.providerHealthEnabled} onChange={toggle('providerHealthEnabled')} note="One anonymous reachability check per enabled provider every two minutes." />
          <ToggleField label="Animations" checked={settings.animationsEnabled} onChange={toggle('animationsEnabled')} note="The system's reduced-motion setting always wins over this switch." />
        </SettingsSection>

        <SettingsSection icon={<ArchiveRestore />} title="Backup" description="Carry providers, keys, routes and model prices to another machine or another install.">
          <BackupPanel />
        </SettingsSection>

        <section className={styles.invariant}><History size={18} /><div><strong>Security boundaries are fixed</strong><p>Loopback binding, remote HTTPS, secret redaction, body/frame caps{__OWNER_EDITION__ ? ' and fail-closed tunnel sanitization' : ''} cannot be disabled from Settings.</p></div></section>
      </div>
    </form>
  )
}

/** Plain-text backup, on request: one button writes the file, one file picker
 * restores it. The warning about the format is stated here and in the file
 * itself, because the operator asked for exactly this trade. Exported for its
 * test, like the form itself. */
const idleBackup = { exporting: false, importing: false, lastExportPath: '', lastReport: null, error: '' }
// Module-level so the hook sees the same function identities on every render
// while no store exists: rebuilt closures would re-subscribe each time.
const idleBackupSubscribe = (): (() => void) => () => {}
const idleBackupSnapshot = (): typeof idleBackup => idleBackup

export function BackupPanel() {
  const [copied, setCopied] = useState(false)
  const importInput = useRef<HTMLInputElement | null>(null)
  // The form is also rendered in isolation by its own tests, without the
  // services context; the panel then shows its explanation and no controls.
  const services = useContext(ServicesContext)
  const backup = services?.backup ?? null
  // The backup model notifies like every other store, and this panel is the one
  // place that read a bare snapshot() instead of subscribing: the model's own
  // re-render was the only thing that could show "Writing…", the export path or
  // the restore report, and nothing triggered one — the feedback stayed in the
  // model until some unrelated render came by, which never came.
  const state = useSyncExternalStore(backup?.subscribe ?? idleBackupSubscribe, backup?.snapshot ?? idleBackupSnapshot)
  if (!backup) {
    return (
      <div className={styles.backupNote}>
        <strong>Plain text, no passphrase</strong>
        <p>One button writes providers, keys and routes to a JSON file you can open anywhere; a file picker restores them onto a new machine. Entries that already exist are skipped.</p>
      </div>
    )
  }

  const pickFile = (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.currentTarget.files?.[0]
    event.currentTarget.value = ''
    if (!file) return
    void file.text().then((content) => void backup.import(content))
  }

  const copyPath = async () => {
    try {
      await navigator.clipboard.writeText(state.lastExportPath)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 2_000)
    } catch {
      // The path stays selectable in the notice either way.
    }
  }

  return (
    <>
      <div className={styles.backupRow}>
        <Button type="button" disabled={state.exporting} onClick={() => void backup.export()}>
          <Database size={15} aria-hidden="true" />
          {state.exporting ? 'Writing…' : 'Export backup'}
        </Button>
        <Button type="button" variant="secondary" disabled={state.importing} onClick={() => importInput.current?.click()}>
          <ArchiveRestore size={15} aria-hidden="true" />
          {state.importing ? 'Restoring…' : 'Restore from file'}
        </Button>
        <input ref={importInput} type="file" accept=".json,application/json" className="sr-only" aria-label="Backup file" onChange={pickFile} />
      </div>
      <div className={styles.backupNote}>
        <strong>Plain text, no passphrase</strong>
        <p>The file carries every key in the clear — that is what makes it openable anywhere. Keep it wherever you trust, and move it off shared storage. Restoring adds what a machine lacks: entries that already exist are skipped, nothing is deleted.</p>
      </div>
      {state.lastExportPath ? (
        <div className={styles.backupOutcome} role="status">
          <span>Backup written to <code>{state.lastExportPath}</code></span>
          <Button type="button" variant="ghost" onClick={() => void copyPath()}>{copied ? 'Copied' : 'Copy path'}</Button>
        </div>
      ) : null}
      {state.lastReport ? (
        <div className={styles.backupOutcome} role="status" data-report>
          <span>
            Restored {state.lastReport.providersAdded} provider{state.lastReport.providersAdded === 1 ? '' : 's'},
            {' '}{state.lastReport.keysAdded} key{state.lastReport.keysAdded === 1 ? '' : 's'},
            {' '}{state.lastReport.routesAdded} route{state.lastReport.routesAdded === 1 ? '' : 's'}
            {state.lastReport.pricesRestored > 0 ? `, ${state.lastReport.pricesRestored} price${state.lastReport.pricesRestored === 1 ? '' : 's'}` : ''}
            {state.lastReport.providersSkipped + state.lastReport.keysSkipped + state.lastReport.routesSkipped > 0 ? ` (already present: ${state.lastReport.providersSkipped + state.lastReport.keysSkipped + state.lastReport.routesSkipped})` : ''}
            {state.lastReport.failed > 0 ? `, ${state.lastReport.failed} entries could not be restored` : ''}
            .
          </span>
        </div>
      ) : null}
      {state.error ? <div className={styles.error} role="alert">{state.error}</div> : null}
    </>
  )
}

function SettingsSection({ icon, title, description, children }: { icon: ReactNode; title: string; description: string; children: ReactNode }) {
  return <section className={styles.section}><header><span>{icon}</span><div><h2>{title}</h2><p>{description}</p></div></header><div className={styles.fields}>{children}</div></section>
}

function NumberField({ label, value, min, max, suffix, note, onChange }: { label: string; value: number; min: number; max: number; suffix?: string; note?: string; onChange: (event: ChangeEvent<HTMLInputElement>) => void }) {
  return <label className={styles.field}><span>{label}</span><span className={styles.inputWrap}><input type="number" value={value} min={min} max={max} step="1" onChange={onChange} />{suffix ? <small>{suffix}</small> : null}</span>{note ? <em>{note}</em> : null}</label>
}

function ToggleField({ label, checked, note, onChange }: { label: string; checked: boolean; note?: string; onChange: () => void }) {
  return (
    <div className={styles.field}>
      <span>{label}</span>
      <button
        type="button"
        className={styles.switch}
        role="switch"
        aria-checked={checked}
        aria-label={label}
        onClick={onChange}
      >
        <span className={styles.knob} aria-hidden="true" />
      </button>
      {note ? <em>{note}</em> : null}
    </div>
  )
}
