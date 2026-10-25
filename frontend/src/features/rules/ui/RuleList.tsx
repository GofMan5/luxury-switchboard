import { ChevronDown, ChevronUp, Pencil, Trash2 } from 'lucide-react'
import { useEffect, useRef, type ReactNode } from 'react'
import { Pill } from '../../../shared/ui/chrome'
import type { CounterText } from './rule-text'
import { ownerLabel } from './rule-text'
import { Switch } from './RuleFields'
import styles from './RulesPage.module.css'

export interface RuleRowView {
  readonly id: string
  readonly name: string
  readonly enabled: boolean
  readonly owner?: string
  readonly match: string
  readonly effects: readonly { readonly text: string; readonly danger?: boolean }[]
  readonly counters: CounterText
}

/**
 * An ordered rule list: position, steppers, switch, summary, counters, edit
 * and delete. While a change is in flight every control stays focusable but
 * ignores presses, so a keyboard user never loses their place.
 */
export function RuleList({ kind, rows, pending, onMove, onToggle, onEdit, onDelete, footer }: {
  readonly kind: string
  readonly rows: readonly RuleRowView[]
  /** The rule id a change is in flight for; `*` for a reorder. */
  readonly pending: string
  readonly onMove: (id: string, direction: -1 | 1) => Promise<boolean>
  readonly onToggle: (id: string, enabled: boolean) => void
  readonly onEdit: (id: string) => void
  readonly onDelete: (id: string) => void
  readonly footer?: ReactNode
}) {
  const listRef = useRef<HTMLOListElement>(null)
  // After a move the row sits elsewhere; put focus back on the stepper pressed,
  // or on the other one when the row reached an end.
  const refocus = useRef<{ id: string; direction: -1 | 1 } | null>(null)
  const busy = pending !== ''

  useEffect(() => {
    const target = refocus.current
    if (!target || busy) return
    refocus.current = null
    const find = (direction: -1 | 1) => [...(listRef.current?.querySelectorAll<HTMLButtonElement>('[data-move]') ?? [])]
      .find((button) => button.dataset.move === `${target.id}:${direction}`)
    const button = find(target.direction)
    ;(button && !button.disabled ? button : find(target.direction === -1 ? 1 : -1))?.focus()
  }, [rows, busy])

  const move = async (id: string, direction: -1 | 1) => {
    if (busy) return
    refocus.current = { id, direction }
    await onMove(id, direction)
  }

  return (
    <div className={styles.listWrap}>
      <ol ref={listRef} className={styles.list} aria-label={`${kind} rules, in evaluation order`}>
        {rows.map((row, index) => {
          const owner = ownerLabel(row.owner)
          const rowBusy = pending === row.id || pending === '*'
          return (
            <li key={row.id} className={styles.row} data-enabled={row.enabled} data-pending={rowBusy || undefined} aria-busy={rowBusy || undefined}>
              <div className={styles.order}>
                <span className={styles.position} aria-hidden="true">{index + 1}</span>
                <button type="button" className={styles.iconButton} data-move={`${row.id}:-1`} aria-label={`Move ${row.name} up`} disabled={index === 0} aria-disabled={busy || undefined} onClick={() => void move(row.id, -1)}><ChevronUp /></button>
                <button type="button" className={styles.iconButton} data-move={`${row.id}:1`} aria-label={`Move ${row.name} down`} disabled={index === rows.length - 1} aria-disabled={busy || undefined} onClick={() => void move(row.id, 1)}><ChevronDown /></button>
              </div>
              <Switch checked={row.enabled} label={`${row.name} enabled`} busy={pending === row.id} disabled={busy} onChange={(enabled) => onToggle(row.id, enabled)} />
              <div className={styles.summary}>
                <div className={styles.titleLine}>
                  <strong>{row.name}</strong>
                  {owner ? <Pill tone="info">{owner}</Pill> : null}
                  {row.enabled ? null : <Pill>Off</Pill>}
                </div>
                <span className={styles.matchLine}>{row.match}</span>
                {row.effects.length ? (
                  <div className={styles.effects}>
                    {row.effects.map((effect) => <span key={effect.text} className={styles.chip} data-tone={effect.danger ? 'danger' : undefined}>{effect.text}</span>)}
                  </div>
                ) : null}
              </div>
              <div className={styles.counters} aria-label={`${row.name} counters`}>
                <span>{row.counters.hits}</span>
                {row.counters.window ? <span>{row.counters.window}</span> : null}
                {row.counters.detail ? <small>{row.counters.detail}</small> : null}
              </div>
              <div className={styles.actions}>
                <button type="button" className={styles.iconButton} aria-label={`Edit ${row.name}`} onClick={() => onEdit(row.id)}><Pencil /></button>
                <button type="button" className={styles.iconButton} data-danger="" aria-label={`Delete ${row.name}`} aria-disabled={busy || undefined} onClick={() => { if (!busy) onDelete(row.id) }}><Trash2 /></button>
              </div>
            </li>
          )
        })}
      </ol>
      {footer ? <div className={styles.listFooter}>{footer}</div> : null}
    </div>
  )
}
