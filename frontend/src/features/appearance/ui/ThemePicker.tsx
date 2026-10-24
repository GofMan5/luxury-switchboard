import { Check } from 'lucide-react'
import type { ThemeChoice } from '../domain/appearance'
import { SYSTEM_THEME_ID } from '../domain/themes'
import styles from './AppearancePage.module.css'

/** Theme tiles as a native radio group: arrow keys move, Space selects, and
 * each tile is a miniature of the window in that palette. */
export function ThemePicker({ choices, value, onChange }: {
  readonly choices: readonly ThemeChoice[]
  readonly value: string
  readonly onChange: (id: string) => void
}) {
  const dark = choices.find((choice) => choice.id === 'graphite')
  const light = choices.find((choice) => choice.id === 'paper')
  return (
    <fieldset className={styles.themeGrid}>
      <legend className="sr-only">Theme</legend>
      {dark && light ? (
        <ThemeTile id={SYSTEM_THEME_ID} name="System" detail="Graphite or Paper" checked={value === SYSTEM_THEME_ID} onChange={onChange}>
          <span className={styles.splitPreview}>
            <Miniature swatch={dark.swatch} />
            <Miniature swatch={light.swatch} />
          </span>
        </ThemeTile>
      ) : null}
      {choices.map((choice) => (
        <ThemeTile key={choice.id} id={choice.id} name={choice.name} detail={choice.builtin ? (choice.scheme === 'light' ? 'Light' : 'Dark') : 'Plugin'} checked={value === choice.id} onChange={onChange}>
          <Miniature swatch={choice.swatch} />
        </ThemeTile>
      ))}
    </fieldset>
  )
}

function ThemeTile({ id, name, detail, checked, onChange, children }: {
  readonly id: string
  readonly name: string
  readonly detail: string
  readonly checked: boolean
  readonly onChange: (id: string) => void
  readonly children: React.ReactNode
}) {
  return (
    <label className={styles.themeTile} data-checked={checked || undefined}>
      <input type="radio" name="theme" value={id} checked={checked} onChange={() => onChange(id)} className="sr-only" />
      {children}
      <span className={styles.themeMeta}>
        <span className={styles.themeName}>{name}</span>
        <span className={styles.themeDetail}>{detail}</span>
        {checked ? <Check className={styles.themeCheck} size={13} aria-hidden="true" /> : null}
      </span>
    </label>
  )
}

/** Canvas, a surface panel with two text lines and an accent control. */
function Miniature({ swatch }: { readonly swatch: readonly string[] }) {
  const [canvas, surface, text, accent] = swatch
  return (
    <span className={styles.miniature} style={{ background: canvas }} aria-hidden="true">
      <span className={styles.miniRail} style={{ background: `color-mix(in srgb, ${canvas} 70%, #000)` }} />
      <span className={styles.miniPanel} style={{ background: surface, borderColor: `color-mix(in srgb, ${text} 12%, transparent)` }}>
        <span style={{ background: text, width: '58%' }} />
        <span style={{ background: text, width: '36%', opacity: 0.45 }} />
        <span className={styles.miniAccent} style={{ background: accent }} />
      </span>
    </span>
  )
}
