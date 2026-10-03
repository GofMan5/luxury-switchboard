import type { ComponentType, PropsWithChildren, ReactNode } from 'react'
import styles from './chrome.module.css'

/** The workspace card: header with title/subtitle/actions plus the content the
 * caller frames. Every panel in the app is this one shape. */
export function Panel({ title, subtitle, actions, className = '', label, children }: PropsWithChildren<{
  readonly title: ReactNode
  readonly subtitle?: ReactNode
  readonly actions?: ReactNode
  readonly className?: string
  /** Accessible region name when the visible title is not the whole answer. */
  readonly label?: string
}>) {
  return (
    <section className={`${styles.panel} ${className}`} aria-label={label}>
      <header className={styles.panelHeader}>
        <div className={styles.panelTitle}>
          <h2>{title}</h2>
          {subtitle ? <p>{subtitle}</p> : null}
        </div>
        {actions ? <div className={styles.panelActions}>{actions}</div> : null}
      </header>
      {children}
    </section>
  )
}

export type MetricTone = 'success' | 'warning' | 'danger'

/** Label / value / detail as one strip of divided cells. */
export function MetricStrip({ children }: PropsWithChildren) {
  return <div className={styles.metricStrip}>{children}</div>
}

export function Metric({ label, value, detail, tone }: { readonly label: string; readonly value: ReactNode; readonly detail?: ReactNode; readonly tone?: MetricTone }) {
  return (
    <div className={styles.metric} data-tone={tone}>
      <span className={styles.metricLabel}>{label}</span>
      <strong className={styles.metricValue}>{value}</strong>
      {detail ? <small className={styles.metricDetail}>{detail}</small> : null}
    </div>
  )
}

export interface SegmentedOption<T extends string> {
  readonly id: T
  readonly label: ReactNode
}

/** One-choice-of-few control: periods, route targets, catalog filters. */
export function Segmented<T extends string>({ options, value, disabled = false, onChange, label }: {
  readonly options: readonly SegmentedOption<T>[]
  readonly value: T
  readonly disabled?: boolean
  readonly onChange: (value: T) => void
  readonly label: string
}) {
  return (
    <div className={styles.segmented} role="group" aria-label={label}>
      {options.map((option) => (
        <button
          key={option.id}
          type="button"
          data-active={option.id === value || undefined}
          aria-pressed={option.id === value}
          disabled={disabled}
          onClick={() => onChange(option.id)}
        >
          {option.label}
        </button>
      ))}
    </div>
  )
}

export type PillTone = 'neutral' | 'success' | 'info' | 'warning' | 'danger'

/** Small labelled state chip for tables and cards. */
export function Pill({ tone = 'neutral', children }: PropsWithChildren<{ readonly tone?: PillTone }>) {
  return <span className={styles.pill} data-tone={tone === 'neutral' ? undefined : tone}>{children}</span>
}

/** The quiet "nothing here yet" block: icon, title, and what happens next. */
export function EmptyState({ icon: Icon, title, hint, height = 160 }: {
  readonly icon: ComponentType<{ size?: number; strokeWidth?: number }>
  readonly title: string
  readonly hint?: string
  readonly height?: number
}) {
  return (
    <div className={styles.emptyState} style={{ minHeight: height }}>
      <Icon size={22} strokeWidth={1.6} />
      <strong>{title}</strong>
      {hint ? <p>{hint}</p> : null}
    </div>
  )
}
