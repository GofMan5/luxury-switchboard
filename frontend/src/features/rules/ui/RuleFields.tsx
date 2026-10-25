import { useId, type ReactNode } from 'react'
import styles from './RulesPage.module.css'

/** What a control needs to be named, described and marked invalid. */
export interface FieldControl {
  readonly id: string
  readonly 'aria-invalid'?: true
  readonly 'aria-describedby'?: string
}

/** A labelled control with a hint, or the reason it was refused instead. */
export function Field({ label, hint, error, className = '', children }: {
  readonly label: string
  readonly hint?: ReactNode
  readonly error?: string
  readonly className?: string
  readonly children: (control: FieldControl) => ReactNode
}) {
  const id = useId()
  const note = error || hint ? `${id}-note` : undefined
  return (
    <div className={`${styles.field} ${className}`} data-invalid={error ? '' : undefined}>
      <label htmlFor={id}>{label}</label>
      {children({ id, ...(error ? { 'aria-invalid': true as const } : {}), ...(note ? { 'aria-describedby': note } : {}) })}
      {error ? <small id={note} className={styles.fieldError}>{error}</small> : hint ? <small id={note}>{hint}</small> : null}
    </div>
  )
}

export function Switch({ checked, label, busy = false, disabled = false, onChange }: {
  readonly checked: boolean
  readonly label: string
  readonly busy?: boolean
  /** Another change is in flight: the switch keeps focus but ignores presses. */
  readonly disabled?: boolean
  readonly onChange: (checked: boolean) => void
}) {
  return (
    <button
      type="button"
      role="switch"
      className={styles.switch}
      aria-checked={checked}
      aria-label={label}
      aria-busy={busy || undefined}
      aria-disabled={disabled || undefined}
      data-switch=""
      onClick={() => { if (!disabled) onChange(!checked) }}
    >
      <span className={styles.knob} aria-hidden="true" />
    </button>
  )
}
