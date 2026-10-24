import { useMemo, useRef, useState, useSyncExternalStore, type ChangeEvent, type KeyboardEvent, type ReactNode } from 'react'
import { Clipboard, Download, LifeBuoy, RotateCcw, Upload } from 'lucide-react'
import { useAppServices } from '../../../app/services'
import { Panel, Segmented } from '../../../shared/ui/chrome'
import { Button } from '../../../shared/ui/Button'
import { restartInSafeMode, safeMode } from '../../../shared/runtime/safe-mode'
import {
  computeTokens,
  CUSTOM_CSS_BYTES,
  customCssProblem,
  MONO_FONT_STACKS,
  resolveTheme,
  themeChoices,
  UI_FONT_STACKS,
  type Appearance,
  type Density,
  type MonoFont,
  type RadiusScale,
  type TextSize,
  type UiFont,
} from '../domain/appearance'
import { parseAppearanceFile, serializeAppearance } from '../domain/appearance-file'
import { BUILTIN_THEMES } from '../domain/themes'
import { ThemePicker } from './ThemePicker'
import { TokenEditor } from './TokenEditor'
import { useAppearance } from './useAppearance'
import { usePrefersLight } from './usePrefersLight'
import styles from './AppearancePage.module.css'

/** A short, hand-tuned set: each holds 4.5:1 for its label on every builtin
 * theme's primary button. Anything else is one colour picker away. */
const ACCENTS: readonly { readonly value: string; readonly name: string }[] = [
  { value: '#ff8a3d', name: 'Ember' },
  { value: '#e9b949', name: 'Brass' },
  { value: '#6fcf8e', name: 'Signal' },
  { value: '#45c1b6', name: 'Lagoon' },
  { value: '#5ea3f7', name: 'Cobalt' },
  { value: '#9b8cf5', name: 'Iris' },
  { value: '#ee6f97', name: 'Rose' },
  { value: '#d7dae0', name: 'Steel' },
]

const DENSITIES: readonly { id: Density; label: string }[] = [
  { id: 'compact', label: 'Compact' },
  { id: 'comfortable', label: 'Default' },
  { id: 'spacious', label: 'Roomy' },
]
const TEXT_SIZES: readonly { id: `${TextSize}`; label: string }[] = [
  { id: '12', label: '12' },
  { id: '13', label: '13' },
  { id: '14', label: '14' },
]
const RADII: readonly { id: RadiusScale; label: string }[] = [
  { id: 'sharp', label: 'Sharp' },
  { id: 'default', label: 'Soft' },
  { id: 'round', label: 'Round' },
]
const UI_FONTS: readonly { id: UiFont; label: string }[] = [
  { id: 'plex', label: 'IBM Plex Sans' },
  { id: 'geist', label: 'Geist' },
  { id: 'system', label: 'System' },
]
const MONO_FONTS: readonly { id: MonoFont; label: string }[] = [
  { id: 'plex', label: 'IBM Plex Mono' },
  { id: 'jetbrains', label: 'JetBrains Mono' },
  { id: 'system', label: 'System' },
]

export default function AppearancePage() {
  const { model, state } = useAppearance()
  const { extensions } = useAppServices()
  const contributions = useSyncExternalStore(extensions.subscribe, extensions.snapshot)
  const light = usePrefersLight()
  const { appearance } = state
  const choices = useMemo(() => themeChoices(contributions.themes, BUILTIN_THEMES), [contributions.themes])
  const theme = useMemo(() => resolveTheme(appearance.theme, contributions.themes, light), [appearance.theme, contributions.themes, light])
  // The table's placeholders show what the window would use with no override.
  const resolved = useMemo(() => computeTokens(appearance, theme, false), [appearance, theme])
  const set = (change: Partial<Appearance>) => model.update(change)

  return (
    <section className={styles.page}>
      <header className="page-header">
        <div>
          <h1>Appearance</h1>
          <p>Every change applies to this window as you make it{state.durable ? ' and is saved with your configuration' : ''}.</p>
        </div>
        <div className={styles.headerStatus} role="status">
          <span className={styles.saveState} data-state={state.pending ? 'pending' : state.error ? 'error' : 'saved'}>
            {state.pending ? 'Saving…' : state.durable ? 'Saved' : 'Stored in this browser profile'}
          </span>
          <Button variant="ghost" onClick={() => model.reset()} title="Theme, controls, tokens and custom CSS back to defaults">
            <RotateCcw size={13} aria-hidden="true" />Reset all
          </Button>
        </div>
      </header>
      {state.error ? (
        <div className={styles.banner} role="alert">
          <span>{state.error}</span>
          <button type="button" className={styles.linkButton} onClick={() => model.dismissError()}>Dismiss</button>
        </div>
      ) : null}
      {safeMode ? (
        <div className={styles.banner} data-tone="warning" role="status">
          <LifeBuoy size={14} aria-hidden="true" />
          <span>Safe mode: custom CSS, token overrides, plugin themes and plugins are off. You can still edit them here.</span>
          <button type="button" className={styles.linkButton} onClick={() => restartInSafeMode(false)}>Restart normally</button>
        </div>
      ) : null}

      <div className={styles.body}>
        <Panel title="Theme" subtitle={appearance.theme === 'system' ? `Following the system — now ${light ? 'light' : 'dark'}` : `${theme.name} · ${theme.scheme}`}>
          <ThemePicker choices={choices} value={appearance.theme} onChange={(id) => set({ theme: id })} />
        </Panel>

        <Panel title="Interface" subtitle="Accent, density, type and corners">
          <div className={styles.rows}>
            <Row label="Accent" hint="Primary actions, focus, selection and the active page.">
              <div className={styles.accents} role="radiogroup" aria-label="Accent colour">
                <AccentChip value="" name={`Theme default (${theme.tokens['--accent'] ?? ''})`} color={theme.tokens['--accent'] ?? ''} checked={!appearance.accent} onSelect={() => set({ accent: '' })} />
                {ACCENTS.map((accent) => (
                  <AccentChip key={accent.value} value={accent.value} name={accent.name} color={accent.value} checked={appearance.accent === accent.value} onSelect={() => set({ accent: accent.value })} />
                ))}
                <label className={styles.accentCustom} data-checked={(appearance.accent && !ACCENTS.some((accent) => accent.value === appearance.accent)) || undefined} title="Any colour">
                  <span className="sr-only">Custom accent colour</span>
                  <input type="color" value={appearance.accent || resolved['--accent'] || '#ff8a3d'} onChange={(event) => set({ accent: event.target.value })} />
                  <span className={styles.accentHex}>{appearance.accent || 'Custom'}</span>
                </label>
              </div>
            </Row>
            <Row label="Density" hint="Row, control and padding scale.">
              <Segmented label="Density" options={DENSITIES} value={appearance.density} onChange={(density) => set({ density })} />
            </Row>
            <Row label="Text size" hint="Base size in pixels; every step scales with it.">
              <Segmented label="Text size" options={TEXT_SIZES} value={`${appearance.textSize}`} onChange={(size) => set({ textSize: Number(size) as TextSize })} />
            </Row>
            <Row label="Interface font" hint="Labels, copy and headings.">
              <Segmented label="Interface font" options={UI_FONTS.map((font) => ({ id: font.id, label: <span style={{ fontFamily: UI_FONT_STACKS[font.id] }}>{font.label}</span> }))} value={appearance.uiFont} onChange={(uiFont) => set({ uiFont })} />
            </Row>
            <Row label="Data font" hint="Numbers, addresses, model ids and code.">
              <Segmented label="Data font" options={MONO_FONTS.map((font) => ({ id: font.id, label: <span style={{ fontFamily: MONO_FONT_STACKS[font.id] }}>{font.label}</span> }))} value={appearance.monoFont} onChange={(monoFont) => set({ monoFont })} />
            </Row>
            <Row label="Corners" hint="Controls, panels and dialogs.">
              <Segmented label="Corners" options={RADII} value={appearance.radius} onChange={(radius) => set({ radius })} />
            </Row>
          </div>
        </Panel>

        <Panel title="Tokens" subtitle="Override any design token by name. Empty fields follow the theme.">
          <TokenEditor overrides={appearance.tokens} resolved={resolved} onChange={(tokens) => set({ tokens })} />
        </Panel>

        <CustomCss key={appearance.customCss} saved={appearance.customCss} onApply={(customCss) => set({ customCss })} />

        <SharePanel appearance={appearance} onImport={(next) => model.update(next)} />
      </div>
    </section>
  )
}

function Row({ label, hint, children }: { readonly label: string; readonly hint: string; readonly children: ReactNode }) {
  return (
    <div className={styles.row}>
      <div className={styles.rowLabel}>
        <span>{label}</span>
        <small>{hint}</small>
      </div>
      <div className={styles.rowControl}>{children}</div>
    </div>
  )
}

function AccentChip({ value, name, color, checked, onSelect }: { readonly value: string; readonly name: string; readonly color: string; readonly checked: boolean; readonly onSelect: () => void }) {
  return (
    <label className={styles.accentChip} data-checked={checked || undefined} data-default={value === '' || undefined} title={name}>
      <input type="radio" name="accent" value={value} checked={checked} onChange={onSelect} className="sr-only" />
      <span className={styles.accentDot} style={{ background: color }} aria-hidden="true" />
      <span className="sr-only">{name}</span>
    </label>
  )
}

function CustomCss({ saved, onApply }: { readonly saved: string; readonly onApply: (css: string) => boolean }) {
  const [draft, setDraft] = useState(saved)
  const bytes = useMemo(() => new TextEncoder().encode(draft).length, [draft])
  const problem = customCssProblem(draft)
  const dirty = draft !== saved
  const apply = () => { if (!problem && dirty) onApply(draft) }
  const keydown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if ((event.ctrlKey || event.metaKey) && event.key === 'Enter') { event.preventDefault(); apply() }
  }
  return (
    <Panel
      title="Custom CSS"
      subtitle="Applied last, after themes and plugin styles. Ctrl+Enter applies."
      actions={(
        <>
          <Button variant="ghost" disabled={!dirty} onClick={() => setDraft(saved)}>Revert</Button>
          <Button variant="primary" disabled={!dirty || Boolean(problem)} onClick={apply}>Apply</Button>
        </>
      )}
    >
      <div className={styles.cssEditor}>
        <label className="sr-only" htmlFor="appearance-custom-css">Custom stylesheet</label>
        <textarea
          id="appearance-custom-css"
          value={draft}
          spellCheck={false}
          autoCapitalize="off"
          autoCorrect="off"
          rows={10}
          aria-invalid={Boolean(problem) || undefined}
          aria-describedby="appearance-custom-css-meta"
          placeholder={'/* Every token is a custom property: */\n:root { --row-height: 30px; }\n\n/* Stable hooks: [data-theme], [data-scheme], [data-density] on <html>. */\n.page-header h1 { letter-spacing: -0.02em; }'}
          onChange={(event) => setDraft(event.target.value)}
          onKeyDown={keydown}
        />
        <div id="appearance-custom-css-meta" className={styles.cssMeta}>
          {problem ? <span className={styles.fieldError}>{problem}</span> : <span>{dirty ? 'Unapplied changes' : saved ? 'Applied' : 'No custom stylesheet'}</span>}
          <span className={styles.byteCount} data-over={bytes > CUSTOM_CSS_BYTES || undefined}>{(bytes / 1024).toFixed(1)} / {CUSTOM_CSS_BYTES / 1024} KiB</span>
        </div>
      </div>
    </Panel>
  )
}

function SharePanel({ appearance, onImport }: { readonly appearance: Appearance; readonly onImport: (next: Appearance) => boolean }) {
  const fileInput = useRef<HTMLInputElement>(null)
  const [notice, setNotice] = useState<{ tone: 'ok' | 'error'; text: string } | null>(null)

  const download = () => {
    const url = URL.createObjectURL(new Blob([serializeAppearance(appearance)], { type: 'application/json' }))
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = 'switchboard-appearance.json'
    anchor.click()
    window.setTimeout(() => URL.revokeObjectURL(url), 1000)
    setNotice({ tone: 'ok', text: 'Appearance file downloaded' })
  }
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(serializeAppearance(appearance))
      setNotice({ tone: 'ok', text: 'Appearance copied as JSON' })
    } catch {
      setNotice({ tone: 'error', text: 'The clipboard is not available here — use the file instead' })
    }
  }
  const load = (text: string) => {
    const parsed = parseAppearanceFile(text)
    if (!parsed.ok) { setNotice({ tone: 'error', text: parsed.error }); return }
    setNotice(onImport(parsed.appearance) ? { tone: 'ok', text: 'Appearance imported and applied' } : { tone: 'error', text: 'The imported appearance was refused' })
  }
  const pick = (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0]
    event.target.value = ''
    if (file) void file.text().then(load, () => setNotice({ tone: 'error', text: 'The file could not be read' }))
  }
  const paste = async () => {
    try { load(await navigator.clipboard.readText()) } catch { setNotice({ tone: 'error', text: 'The clipboard is not available here — use the file instead' }) }
  }

  return (
    <Panel title="Share and recover" subtitle="One JSON file carries the theme, controls, tokens and stylesheet — never credentials.">
      <div className={styles.share}>
        <div className={styles.shareActions}>
          <Button onClick={download}><Download size={13} aria-hidden="true" />Export file</Button>
          <Button onClick={() => void copy()}><Clipboard size={13} aria-hidden="true" />Copy JSON</Button>
          <Button onClick={() => fileInput.current?.click()}><Upload size={13} aria-hidden="true" />Import file</Button>
          <Button variant="ghost" onClick={() => void paste()}>Paste JSON</Button>
          <input ref={fileInput} type="file" accept=".json,application/json" className="sr-only" aria-label="Appearance file" tabIndex={-1} onChange={pick} />
        </div>
        {notice ? <p className={styles.notice} data-tone={notice.tone} role={notice.tone === 'error' ? 'alert' : 'status'}>{notice.text}</p> : null}
        <div className={styles.recover}>
          <LifeBuoy size={14} aria-hidden="true" />
          <p>
            A stylesheet or plugin hid the controls? Press <kbd>Ctrl</kbd><kbd>Alt</kbd><kbd>Shift</kbd><kbd>S</kbd> anywhere to restart in safe mode — no custom CSS, overrides or plugins, with this page still working.
          </p>
          <Button variant="ghost" onClick={() => restartInSafeMode(!safeMode)}>{safeMode ? 'Leave safe mode' : 'Restart in safe mode'}</Button>
        </div>
      </div>
    </Panel>
  )
}
