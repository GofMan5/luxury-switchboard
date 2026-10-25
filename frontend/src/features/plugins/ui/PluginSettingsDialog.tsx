import { useEffect, useId, useState, type FormEvent } from 'react'
import { RotateCcw, X } from 'lucide-react'
import { Button } from '../../../shared/ui/Button'
import { useModalFocus } from '../../../shared/ui/useModalFocus'
import type { PluginSettingsView, PluginsModel } from '../application/plugins-model'
import type { PluginSummary } from '../domain/plugin'
import { SECRET_VALUE_LIMIT, isSecretSetting, secretProblem, valueProblem, type SecretSetting, type SettingValue, type ValueSetting } from '../domain/settings'
import { useStyleHold } from './useStyleHold'
import styles from './PluginsPage.module.css'

/** What a field holds while it is edited: numbers stay text until saved, so a
 * half-typed `-` or `1e` is not thrown away. */
type Draft = string | boolean

type Load =
  | { readonly phase: 'loading' }
  | { readonly phase: 'error'; readonly message: string }
  | { readonly phase: 'ready'; readonly view: PluginSettingsView }

/**
 * The operator's editor for the options a plugin declares. Values are checked
 * against the declaration before one atomic save; a running plugin hears the
 * change at once, without a restart.
 */
export function PluginSettingsDialog({ plugin, model, pending, onClose, onSaved }: {
  readonly plugin: PluginSummary
  readonly model: Pick<PluginsModel, 'loadSettings' | 'saveSettings' | 'secretSet'>
  readonly pending: boolean
  readonly onClose: () => void
  readonly onSaved: () => void
}) {
  const ref = useModalFocus<HTMLFormElement>(onClose, pending)
  const id = useId()
  const [load, setLoad] = useState<Load>({ phase: 'loading' })
  const [drafts, setDrafts] = useState<Record<string, Draft>>({})
  const [problems, setProblems] = useState<Record<string, string>>({})
  const [error, setError] = useState('')
  const [attempt, setAttempt] = useState(0)
  useStyleHold()

  useEffect(() => {
    let live = true
    setLoad({ phase: 'loading' })
    model.loadSettings(plugin.id).then((view) => {
      if (!live) return
      setLoad({ phase: 'ready', view })
      setDrafts(toDrafts(view.settings.filter((setting): setting is ValueSetting => !isSecretSetting(setting)), view.values))
    }, (reason: unknown) => {
      if (live) setLoad({ phase: 'error', message: reason instanceof Error && reason.message ? reason.message : 'The settings could not be loaded.' })
    })
    return () => { live = false }
  }, [model, plugin.id, attempt])

  const declared = load.phase === 'ready' ? load.view.settings : []
  const settings = declared.filter((setting): setting is ValueSetting => !isSecretSetting(setting))
  const secrets = declared.filter(isSecretSetting)
  const saved = load.phase === 'ready' ? toDrafts(settings, load.view.values) : {}
  const dirty = settings.some((setting) => drafts[setting.key] !== saved[setting.key])
  const atDefaults = settings.every((setting) => drafts[setting.key] === draftOf(setting, setting.default))

  const edit = (key: string, value: Draft) => {
    setDrafts((current) => ({ ...current, [key]: value }))
    setProblems((current) => {
      if (!current[key]) return current
      const { [key]: _fixed, ...rest } = current
      return rest
    })
    setError('')
  }

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (pending || load.phase !== 'ready') return
    const values: Record<string, SettingValue> = {}
    const found: Record<string, string> = {}
    for (const setting of settings) {
      const value = fromDraft(setting, drafts[setting.key])
      const problem = valueProblem(setting, value)
      if (problem) found[setting.key] = problem
      else values[setting.key] = value as SettingValue
    }
    setProblems(found)
    const first = settings.find((setting) => found[setting.key])
    if (first) {
      document.getElementById(`${id}-${first.key}`)?.focus()
      return
    }
    const result = await model.saveSettings(plugin.id, settings, values)
    if (result.ok) onSaved()
    else setError(result.message)
  }

  return (
    <div className="ui-scrim">
      <form ref={ref} className={`ui-modal ${styles.dialog} ${styles.settingsDialog}`} role="dialog" aria-modal="true" aria-labelledby={`${id}-title`} aria-busy={pending || load.phase === 'loading' || undefined} noValidate onSubmit={(event) => void submit(event)}>
        <header>
          <div>
            <h2 id={`${id}-title`}>{plugin.name} settings</h2>
            <p>{plugin.enabled ? 'Saved values reach the running plugin right away.' : 'The plugin reads these the next time it starts.'}</p>
          </div>
          <button type="button" aria-label="Close" disabled={pending} onClick={onClose}><X size={16} /></button>
        </header>
        <div className={styles.dialogBody}>
          {load.phase === 'loading' ? <p className={styles.muted} role="status">Loading settings…</p> : null}
          {load.phase === 'error' ? (
            <div className={styles.settingsError} role="alert">
              <p>{load.message}</p>
              <Button type="button" onClick={() => setAttempt((count) => count + 1)}><RotateCcw size={13} aria-hidden="true" />Retry</Button>
            </div>
          ) : null}
          {load.phase === 'ready' && declared.length === 0 ? <p className={styles.muted}>This version of the plugin declares no settings.</p> : null}
          {settings.length > 0 ? (
            <div className={styles.settingList}>
              {settings.map((setting) => (
                <SettingField
                  key={setting.key}
                  id={`${id}-${setting.key}`}
                  setting={setting}
                  draft={drafts[setting.key] ?? draftOf(setting, setting.default)}
                  changed={drafts[setting.key] !== saved[setting.key]}
                  problem={problems[setting.key] ?? ''}
                  disabled={pending}
                  onChange={(value) => edit(setting.key, value)}
                />
              ))}
            </div>
          ) : null}
          {secrets.length > 0 ? (
            <div className={styles.settingList} aria-label="Secrets">
              {secrets.map((setting) => (
                <SecretField key={setting.key} id={`${id}-${setting.key}`} pluginId={plugin.id} setting={setting} set={plugin.secrets.includes(setting.key)} disabled={pending} model={model} />
              ))}
            </div>
          ) : null}
          {error ? <p className={styles.dialogError} role="alert">{error}</p> : null}
        </div>
        <footer>
          {settings.length > 0 ? (
            <Button
              type="button"
              className={styles.footerStart}
              variant="ghost"
              disabled={pending || atDefaults}
              title="Fill every field with the plugin's default; nothing is saved until you press Save"
              onClick={() => { setDrafts(toDrafts(settings, Object.fromEntries(settings.map((setting) => [setting.key, setting.default])))); setProblems({}); setError('') }}
            >
              Reset to defaults
            </Button>
          ) : null}
          <Button type="button" disabled={pending} onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" data-autofocus disabled={pending || load.phase !== 'ready' || !dirty} aria-busy={pending || undefined}>
            {pending ? 'Saving…' : 'Save'}
          </Button>
        </footer>
      </form>
    </div>
  )
}

/**
 * A write-only field: it shows whether a value is set, never the value. A new
 * value goes to the control plane the moment it is saved, apart from the
 * settings form, and is forgotten here.
 */
function SecretField({ id, pluginId, setting, set, disabled, model }: {
  readonly id: string
  readonly pluginId: string
  readonly setting: SecretSetting
  readonly set: boolean
  readonly disabled: boolean
  readonly model: Pick<PluginsModel, 'secretSet'>
}) {
  const [editing, setEditing] = useState(false)
  const [value, setValue] = useState('')
  const [busy, setBusy] = useState(false)
  const [problem, setProblem] = useState('')
  const off = disabled || busy

  const apply = async (next: string | null) => {
    if (next !== null) {
      const found = secretProblem(next)
      if (found) { setProblem(found); return }
    }
    setBusy(true)
    const result = await model.secretSet(pluginId, setting.key, next)
    setBusy(false)
    if (!result.ok) { setProblem(result.message); return }
    setValue('')
    setEditing(false)
    setProblem('')
  }

  const described = [setting.description ? `${id}-hint` : '', problem ? `${id}-problem` : ''].filter(Boolean).join(' ') || undefined
  return (
    <div className={styles.setting} data-type="secret" data-invalid={problem ? true : undefined}>
      <label id={`${id}-label`} htmlFor={editing ? id : undefined} className={styles.settingLabel}>{setting.label}</label>
      <div className={styles.settingControl}>
        {editing ? (
          <span className={styles.secretEdit}>
            <input
              id={id}
              type="password"
              autoComplete="new-password"
              spellCheck={false}
              maxLength={SECRET_VALUE_LIMIT}
              disabled={off}
              aria-invalid={problem ? true : undefined}
              aria-describedby={described}
              value={value}
              autoFocus
              onChange={(event) => { setValue(event.target.value); setProblem('') }}
              onKeyDown={(event) => {
                if (event.key === 'Enter') { event.preventDefault(); void apply(value) }
              }}
            />
            <Button type="button" variant="primary" disabled={off} aria-busy={busy || undefined} onClick={() => void apply(value)}>{busy ? 'Saving…' : 'Save'}</Button>
            <Button type="button" variant="ghost" disabled={off} onClick={() => { setEditing(false); setValue(''); setProblem('') }}>Cancel</Button>
          </span>
        ) : (
          <span className={styles.secretEdit}>
            <span className={styles.secretState} data-set={set || undefined}>{set ? 'Set' : 'Not set'}</span>
            <Button type="button" disabled={off} aria-describedby={`${id}-label`} onClick={() => setEditing(true)}>{set ? 'Replace' : 'Set'}</Button>
            {set ? <Button type="button" variant="ghost" disabled={off} aria-describedby={`${id}-label`} aria-busy={busy || undefined} onClick={() => void apply(null)}>Clear</Button> : null}
          </span>
        )}
      </div>
      {setting.description ? <p id={`${id}-hint`} className={styles.settingHint}>{setting.description} Saved on its own, write-only.</p> : null}
      {problem ? <p id={`${id}-problem`} className={styles.settingProblem} role="alert">{problem}</p> : null}
    </div>
  )
}

function SettingField({ id, setting, draft, changed, problem, disabled, onChange }: {
  readonly id: string
  readonly setting: ValueSetting
  readonly draft: Draft
  readonly changed: boolean
  readonly problem: string
  readonly disabled: boolean
  readonly onChange: (value: Draft) => void
}) {
  const hint = [setting.description, defaultText(setting)].filter(Boolean).join(' ')
  const described = [hint ? `${id}-hint` : '', problem ? `${id}-problem` : ''].filter(Boolean).join(' ') || undefined
  const common = { id, disabled, 'aria-invalid': problem ? true : undefined, 'aria-describedby': described } as const
  let control
  switch (setting.type) {
    case 'toggle':
      control = (
        <button {...common} type="button" role="switch" className={styles.switch} aria-checked={draft === true} aria-labelledby={`${id}-label`} onClick={() => onChange(draft !== true)}>
          <span className={styles.knob} aria-hidden="true" />
        </button>
      )
      break
    case 'select':
      control = (
        <select {...common} value={String(draft)} onChange={(event) => onChange(event.target.value)}>
          {setting.options.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
        </select>
      )
      break
    case 'number':
      control = (
        <input
          {...common}
          type="text"
          inputMode="decimal"
          autoComplete="off"
          spellCheck={false}
          className={styles.settingNumber}
          placeholder={setting.placeholder || undefined}
          value={String(draft)}
          onChange={(event) => onChange(event.target.value)}
        />
      )
      break
    case 'text':
      control = (
        <input
          {...common}
          type="text"
          autoComplete="off"
          maxLength={setting.maxLength}
          placeholder={setting.placeholder || undefined}
          value={String(draft)}
          onChange={(event) => onChange(event.target.value)}
        />
      )
      break
  }
  return (
    <div className={styles.setting} data-type={setting.type} data-changed={changed || undefined} data-invalid={problem ? true : undefined}>
      <label id={`${id}-label`} htmlFor={id} className={styles.settingLabel}>
        {setting.label}
        {changed ? <span className="sr-only"> (changed)</span> : null}
      </label>
      <div className={styles.settingControl}>{control}</div>
      {hint ? <p id={`${id}-hint`} className={styles.settingHint}>{hint}</p> : null}
      {problem ? <p id={`${id}-problem`} className={styles.settingProblem}>{problem}</p> : null}
    </div>
  )
}

function defaultText(setting: ValueSetting): string {
  switch (setting.type) {
    case 'toggle': return `Default: ${setting.default ? 'on' : 'off'}.`
    case 'select': return `Default: ${setting.options.find((option) => option.value === setting.default)?.label ?? setting.default}.`
    case 'number': return `Default: ${setting.default}.`
    case 'text': return setting.default ? `Default: “${setting.default}”.` : ''
  }
}

function draftOf(setting: ValueSetting, value: SettingValue): Draft {
  return setting.type === 'toggle' ? value === true : String(value)
}

function toDrafts(settings: readonly ValueSetting[], values: Readonly<Record<string, SettingValue>>): Record<string, Draft> {
  const drafts: Record<string, Draft> = {}
  for (const setting of settings) drafts[setting.key] = draftOf(setting, values[setting.key] ?? setting.default)
  return drafts
}

/** The draft as the value the declaration checks; a number that does not
 * parse stays text, so the check reports it. */
function fromDraft(setting: ValueSetting, draft: Draft | undefined): unknown {
  if (draft === undefined) return setting.default
  if (setting.type !== 'number' || typeof draft !== 'string') return draft
  const text = draft.trim()
  if (!/^[-+]?(?:\d+\.?\d*|\.\d+)(?:e[-+]?\d+)?$/iu.test(text)) return draft
  return Number(text)
}
