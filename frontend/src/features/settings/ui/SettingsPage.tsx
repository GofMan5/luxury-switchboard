import { useContext, useRef, useState, useSyncExternalStore, type ChangeEvent, type FormEvent, type KeyboardEvent as UIKeyboardEvent, type ReactNode } from 'react'
import { ArchiveRestore, Bell, Database, Gauge, GitBranch, History, RotateCcw, Save, ShieldCheck } from 'lucide-react'
import { Button } from '../../../shared/ui/Button'
import { useMediaQuery } from '../../../shared/ui/useMediaQuery'
import { ServicesContext } from '../../../app/services'
import { SETTINGS_DEFAULTS, type Settings } from '../domain/settings'
import { useSettings } from './useSettings'
import styles from './SettingsPage.module.css'

type SettingsTab = 'relay' | 'reliability' | 'data' | 'routing' | 'interface' | 'backup'

const tabs: readonly { id: SettingsTab; label: string; icon: ReactNode; description: string }[] = [
  { id: 'relay', label: 'Relay', icon: <Gauge size={16} />, description: 'The address your clients call and how much traffic may pile up waiting.' },
  { id: 'reliability', label: 'Reliability', icon: <ShieldCheck size={16} />, description: 'Timeouts and retries shared by every provider profile.' },
  { id: 'data', label: 'Data', icon: <Database size={16} />, description: 'How much the app remembers, in memory and on disk.' },
  { id: 'routing', label: 'Routing', icon: <GitBranch size={16} />, description: 'What happens when a provider answers with a verdict no retry could change.' },
  { id: 'interface', label: 'Interface', icon: <Bell size={16} />, description: 'What the shell tells you as it happens, and how it moves.' },
  { id: 'backup', label: 'Backup', icon: <ArchiveRestore size={16} />, description: 'Carry providers, keys, routes and model prices to another machine or another install.' },
]

/** Which draft fields a tab edits, so its reset touches its own and nothing else. */
const tabFields: Record<Exclude<SettingsTab, 'backup'>, readonly (keyof Settings)[]> = {
  relay: ['listenerPort', 'maxRequestMiB', 'maxQueued'],
  reliability: ['headerTimeoutSeconds', 'streamIdleSeconds', 'retryBaseMilliseconds', 'retryMaxSeconds', 'permanentAttempts'],
  data: ['activityCapacity', 'historyRetentionDays', 'tunnelRetentionHours', 'guardrailFindings'],
  routing: ['failoverEnabled', 'chainMode'],
  interface: ['notificationsEnabled', 'providerHealthEnabled', 'animationsEnabled'],
}

export default function SettingsPage() {
  const { model, state } = useSettings()
  // The tab lives here, not in the keyed form: the key remounts the form when
  // fresh settings land after a save, and a save must not throw the operator
  // back to the first tab.
  const [tab, setTab] = useState<SettingsTab>('relay')
  if (!state.settings) {
    return <section className={styles.page}><header className="page-header"><div><h1>Settings</h1><p>Loading validated runtime configuration…</p></div></header></section>
  }
  return <SettingsForm key={JSON.stringify(state.settings)} initial={state.settings} pending={state.pending} error={state.error} tab={tab} onTab={setTab} onSave={(value) => model.save(value)} />
}

export function SettingsForm({ initial, pending, error, tab: controlledTab, onTab, onSave }: { initial: Settings; pending: boolean; error: string; tab?: SettingsTab; onTab?: (tab: SettingsTab) => void; onSave: (value: Settings) => Promise<boolean> }) {
  const [settings, setSettings] = useState(initial)
  // The page hoists the tab so a post-save remount keeps it; the form rendered
  // standalone (its tests) just keeps its own.
  const [internalTab, setInternalTab] = useState<SettingsTab>('relay')
  const tab = controlledTab ?? internalTab
  const setTab = onTab ?? setInternalTab
  const dirty = JSON.stringify(settings) !== JSON.stringify(initial)
  const set = <K extends keyof Settings>(field: K, value: Settings[K]) => {
    setSettings((current) => ({ ...current, [field]: value }))
  }
  // The tab reset writes the shipped defaults back into the draft; saving is
  // still the operator's call, so an accidental click is one Cancel away.
  const resetTab = () => {
    if (tab === 'backup') return
    setSettings((current) => {
      const next = { ...current }
      for (const field of tabFields[tab]) {
        ;(next as Record<keyof Settings, unknown>)[field] = SETTINGS_DEFAULTS[field]
      }
      return next
    })
  }
  const tabAtDefaults = tab !== 'backup' && tabFields[tab].every((field) => settings[field] === SETTINGS_DEFAULTS[field])
  const submit = (event: FormEvent) => { event.preventDefault(); void onSave(settings) }
  const activeTab = tabs.find((entry) => entry.id === tab) ?? tabs[0]
  // The rail is vertical on wide windows and a horizontal strip under 959px —
  // the orientation is announced, and the arrows follow both axes either way.
  const horizontal = useMediaQuery('(max-width: 959px)')
  const railRef = useRef<HTMLElement>(null)
  const tabKeys = (event: UIKeyboardEvent) => {
    const index = tabs.findIndex((entry) => entry.id === tab)
    let next = -1
    if (event.key === 'ArrowDown' || event.key === 'ArrowRight') next = (index + 1) % tabs.length
    if (event.key === 'ArrowUp' || event.key === 'ArrowLeft') next = (index - 1 + tabs.length) % tabs.length
    if (event.key === 'Home') next = 0
    if (event.key === 'End') next = tabs.length - 1
    if (next < 0) return
    event.preventDefault()
    setTab(tabs[next].id)
    railRef.current?.querySelectorAll<HTMLButtonElement>('[role="tab"]')[next]?.focus()
  }

  const field = <K extends keyof Settings>(key: K) => ({
    value: settings[key],
    saved: initial[key],
    fallback: SETTINGS_DEFAULTS[key],
    disabled: pending,
    onChange: (value: Settings[K]) => set(key, value),
  })

  return (
    <form className={styles.page} onSubmit={submit}>
      <header className="page-header">
        <div><h1>Settings</h1><p>Every value applies the moment you save — no restart</p></div>
        <div className={styles.headerActions}>
          {dirty && !pending ? <Button type="button" variant="ghost" onClick={() => setSettings(initial)}>Discard changes</Button> : null}
          <Button type="submit" variant="primary" disabled={!dirty || pending}><Save size={15} />{pending ? 'Saving…' : 'Save settings'}</Button>
        </div>
      </header>
      {error ? <div className={styles.error} role="alert">{error}</div> : null}

      <div className={styles.layout}>
        <nav ref={railRef} className={styles.tabRail} role="tablist" aria-label="Settings sections" aria-orientation={horizontal ? 'horizontal' : 'vertical'} onKeyDown={tabKeys}>
          {tabs.map((entry) => (
            <button
              key={entry.id}
              type="button"
              role="tab"
              id={`settings-tab-${entry.id}`}
              aria-selected={tab === entry.id}
              aria-controls={tab === entry.id ? `settings-panel-${entry.id}` : undefined}
              tabIndex={tab === entry.id ? 0 : -1}
              data-active={tab === entry.id}
              onClick={() => setTab(entry.id)}
            >
              <span className={styles.tabIcon} aria-hidden="true">{entry.icon}</span>
              {entry.label}
            </button>
          ))}
          <div className={styles.railNote}>
            <History size={14} aria-hidden="true" />
            <p>Security boundaries are fixed: loopback binding, remote HTTPS, secret redaction, body and frame caps{__OWNER_EDITION__ ? ', fail-closed tunnel sanitization' : ''} cannot be disabled from here.</p>
          </div>
        </nav>

        <div className={styles.tabContent} role="tabpanel" id={`settings-panel-${activeTab.id}`} aria-labelledby={`settings-tab-${activeTab.id}`}>
          <header className={styles.tabHeader}>
            <div>
              <h2>{activeTab.label}</h2>
              <p>{activeTab.description}</p>
            </div>
            {tab !== 'backup' ? (
              <Button type="button" variant="ghost" disabled={tabAtDefaults} onClick={resetTab}>
                <RotateCcw size={14} aria-hidden="true" />
                Defaults
              </Button>
            ) : null}
          </header>

          {tab === 'relay' ? (
            <div className={styles.fields}>
              <NumberField
                label="Listener port" min={1} max={65535} presets={[8798, 8787]} {...field('listenerPort')}
                note="The loopback address your clients call: http://127.0.0.1:port. Remote binding is never allowed — only this machine can talk to the relay. Changing the port rebinds the listener in place; requests in flight are cancelled."
              />
              <NumberField
                label="Maximum request" min={1} max={256} suffix="MiB" presets={[32, 64, 128]} {...field('maxRequestMiB')}
                note="The largest single request the relay accepts, headers included. 64 MiB fits a context window far past every current model; lower it only to defend memory on a small machine."
              />
              <NumberField
                label="Maximum queued" min={100} max={100000} presets={[1_000, 10_000, 50_000]} {...field('maxQueued')}
                note="A request may wait for a free key as long as it needs — but the queue itself is bounded: past this many waiting requests, new ones are refused immediately instead of piling into memory."
              />
              <p className={styles.tabNote}>A port change rebinds the listener the moment you save; everything else on this page just starts applying.</p>
            </div>
          ) : null}

          {tab === 'reliability' ? (
            <div className={styles.fields}>
              <NumberField
                label="Response headers" min={5} max={300} suffix="sec" presets={[30, 45, 60, 120]} {...field('headerTimeoutSeconds')}
                note="How long a provider may think before its first byte. Cold requests on big contexts prefill for a while — under ~30 seconds those get cut before they start."
              />
              <NumberField
                label="Stream idle" min={15} max={900} suffix="sec" presets={[60, 300, 900]} {...field('streamIdleSeconds')}
                note="The silence budget inside a live stream: no bytes for this long and the attempt is abandoned and retried on another key. Providers that prefill silently for minutes need this raised."
              />
              <NumberField
                label="Retry base" min={50} max={10000} suffix="ms" presets={[250, 500, 1_000]} {...field('retryBaseMilliseconds')}
                note="The wait before the first retry; every next wait doubles from here. Lower retries faster and leans harder on rate limits."
              />
              <NumberField
                label="Retry maximum" min={1} max={120} suffix="sec" presets={[15, 30, 60]} {...field('retryMaxSeconds')}
                note="The cap the backoff ladder climbs to, so a busy provider does not make a request wait minutes between attempts."
              />
              <NumberField
                label="Permanent attempts" min={1} max={3} presets={[1, 2, 3]} {...field('permanentAttempts')}
                note="How many times a request that failed for good — dead keys, spent quota, a refusal — is rebuilt before you see the error. 429s and broken connections retry separately and never spend this budget."
              />
            </div>
          ) : null}

          {tab === 'data' ? (
            <div className={styles.fields}>
              <NumberField
                label="Live activity rows" min={100} max={20000} presets={[1_000, 2_000, 5_000]} {...field('activityCapacity')}
                note="How many recent requests Live Activity keeps in memory. More rows mean a longer scrollback and a bit more RAM."
              />
              <NumberField
                label="History retention" min={1} max={365} suffix="days" presets={[7, 30, 90]} {...field('historyRetentionDays')}
                note="How long the persisted journal — Insights, token counting, cost — is kept. Shrinking the window prunes the old records right away."
              />
              {/* The public sidecar has no tunnel, so it keeps no tunnel log to retain.
                  The literal is tested here rather than a runtime flag so the field
                  leaves the public bundle entirely. */}
              {__OWNER_EDITION__ ? (
                <NumberField
                  label="Tunnel log retention" min={24} max={720} suffix="hours" presets={[72, 168, 720]} {...field('tunnelRetentionHours')}
                  note="How long the per-client tunnel log is kept. Shrinking it prunes right away."
                />
              ) : null}
              <NumberField
                label="Guardrail findings" min={50} max={5000} presets={[250, 500, 1_000]} {...field('guardrailFindings')}
                note="How many inspection findings are kept. They live in memory only and are never sent anywhere. The inspection mode itself is chosen on the Guardrails page."
              />
            </div>
          ) : null}

          {tab === 'routing' ? (
            <div className={styles.fields}>
              <ToggleField
                label="Failover chain" {...field('failoverEnabled')}
                note="On: a dead provider, an exhausted shared quota, a client-level rejection or a missing model moves the request to the next provider of that model's chain. Off: the refusal reaches your client exactly where it happened."
              />
              <div className={styles.field} data-dirty={settings.chainMode !== initial.chainMode || undefined}>
                <span className={styles.fieldHead}>
                  <span>Chain mode</span>
                  <span className={styles.fieldControl}>
                    <FieldReset show={settings.chainMode !== initial.chainMode} label="Chain mode" onReset={() => set('chainMode', initial.chainMode)} />
                    <span className={styles.inputWrap}>
                      <select value={settings.chainMode} disabled={pending} onChange={(event) => set('chainMode', event.currentTarget.value as Settings['chainMode'])} aria-label="Chain mode">
                        <option value="balance">Balance (round-robin)</option>
                        <option value="failover">Failover (strict order)</option>
                      </select>
                    </span>
                  </span>
                </span>
                <em>Balance spreads requests across every healthy provider of a chain: two providers means twice the daily quota, because the quotas resellers run out of are per provider, not per you. Failover sends everything to the head of the chain and only moves on refusal. Default: Balance.</em>
              </div>
              <div className={styles.explainer}>
                <strong>How a chain works</strong>
                <p>Publish one model on several providers in Model Routes — for example glm → North Relay first, Vendor Hub second. A request for glm lands on North Relay; when North Relay answers with a final verdict (dead keys, a spent quota, a client ban, a model it does not host), the relay degrades North Relay for five minutes, rewrites the request to the upstream name of the sibling and sends it there. Your client never sees the failure. After five minutes the chain tries North Relay again — in Balance mode it shares the load right away. Configure chains on the Model Routes page; models without a chain keep using the active provider.</p>
              </div>
            </div>
          ) : null}

          {tab === 'interface' ? (
            <div className={styles.fields}>
              <ToggleField
                label="Notifications" {...field('notificationsEnabled')}
                note="Toasts and the unread badge. The feed itself stays recorded either way — this switch only governs the interruptions."
              />
              <ToggleField
                label="Provider health probe" {...field('providerHealthEnabled')}
                note="One anonymous reachability check per enabled provider every two minutes. It carries no keys and asks for nothing but a status code."
              />
              <ToggleField
                label="Animations" {...field('animationsEnabled')}
                note="Motion in the interface. The operating system's reduced-motion setting always wins over this switch."
              />
            </div>
          ) : null}

          {tab === 'backup' ? (
            <div className={styles.fields}>
              <BackupPanel />
            </div>
          ) : null}
        </div>
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

function FieldReset({ show, label, onReset }: { show: boolean; label: string; onReset: () => void }) {
  if (!show) return null
  return (
    <button type="button" className={styles.fieldReset} aria-label={`Reset ${label} to the saved value`} title="Back to the saved value" onClick={onReset}>
      <RotateCcw size={12} aria-hidden="true" />
    </button>
  )
}

/** A numeric setting with its explanation, the values that cover most cases,
 * and a way back: to the saved value per field, to the shipped defaults per
 * tab. The input holds raw text while typing — a field you can empty — and the
 * value commits parsed and clamped into the valid range on blur. */
function NumberField({ label, value, saved, fallback, min, max, suffix, note, presets, disabled, onChange }: {
  label: string
  value: number
  saved: number
  fallback: number
  min: number
  max: number
  suffix?: string
  note?: string
  presets?: readonly number[]
  disabled?: boolean
  onChange: (value: number) => void
}) {
  const dirty = value !== saved
  const [draft, setDraft] = useState<string | null>(null)
  const change = (raw: string) => {
    setDraft(raw)
    const parsed = Number(raw)
    if (raw.trim() !== '' && Number.isFinite(parsed)) onChange(parsed)
  }
  const commit = () => {
    if (draft === null) return
    const parsed = Number(draft)
    // An empty or unreadable field returns to the saved value; an out-of-range
    // one lands on the nearest bound rather than shipping a backend refusal.
    onChange(draft.trim() === '' || !Number.isFinite(parsed) ? saved : Math.min(max, Math.max(min, Math.round(parsed))))
    setDraft(null)
  }
  return (
    <div className={styles.field} data-dirty={dirty || undefined}>
      <span className={styles.fieldHead}>
        <span className={styles.fieldLabel}>{label}</span>
        <span className={styles.fieldControl}>
          <FieldReset show={dirty} label={label} onReset={() => onChange(saved)} />
          <span className={styles.inputWrap}>
            <input type="number" value={draft ?? value} min={min} max={max} step="1" aria-label={label} disabled={disabled} onChange={(event) => change(event.currentTarget.value)} onBlur={commit} />
            {suffix ? <small>{suffix}</small> : null}
          </span>
        </span>
      </span>
      {note ? <em>{note}</em> : null}
      {presets && presets.length > 0 ? (
        <span className={styles.presets}>
          {presets.map((preset) => (
            <button
              key={preset}
              type="button"
              className={styles.preset}
              data-active={value === preset || undefined}
              aria-pressed={value === preset}
              disabled={disabled}
              onClick={() => { setDraft(null); onChange(preset) }}
            >
              {preset.toLocaleString('en-US')}
            </button>
          ))}
          <span className={styles.presetDefault}>Default: {fallback.toLocaleString('en-US')}{suffix ? ` ${suffix}` : ''}</span>
        </span>
      ) : null}
    </div>
  )
}

function ToggleField({ label, value, saved, note, disabled, onChange }: { label: string; value: boolean; saved: boolean; note?: string; disabled?: boolean; onChange: (value: boolean) => void }) {
  const dirty = value !== saved
  return (
    <div className={styles.field} data-dirty={dirty || undefined}>
      <span className={styles.fieldHead}>
        <span className={styles.fieldLabel}>{label}</span>
        <span className={styles.fieldControl}>
          <FieldReset show={dirty} label={label} onReset={() => onChange(saved)} />
          <button
            type="button"
            className={styles.switch}
            role="switch"
            aria-checked={value}
            aria-label={label}
            disabled={disabled}
            onClick={() => onChange(!value)}
          >
            <span className={styles.knob} aria-hidden="true" />
          </button>
        </span>
      </span>
      {note ? <em>{note}</em> : null}
    </div>
  )
}
