import { CheckCircle2, Plus } from 'lucide-react'
import { formatClock, formatInteger } from '../../../shared/format/metrics'
import { Button } from '../../../shared/ui/Button'
import type { RecentError } from '../domain/rules'
import { errorKind } from './rule-text'
import styles from './RulesPage.module.css'

/**
 * Failed requests grouped by signature, newest first. Each row either names
 * the custom retry rule that now handles it or offers to create one.
 */
export function RecentErrorsTable({ errors, ruleNames, canCreate, onCreate, onOpenRule }: {
  readonly errors: readonly RecentError[]
  /** Retry rule ids to names; a covering id missing here is shown as text. */
  readonly ruleNames: ReadonlyMap<string, string>
  /** False at the retry-rule limit. */
  readonly canCreate: boolean
  readonly onCreate: (error: RecentError) => void
  readonly onOpenRule: (id: string) => void
}) {
  return (
    <div className={styles.tableWrap}>
      <table className={styles.table}>
        <thead>
          <tr>
            <th scope="col">Error</th>
            <th scope="col">Detail</th>
            <th scope="col">Source</th>
            <th scope="col">Seen</th>
            <th scope="col"><span className="sr-only">Retry rule</span></th>
          </tr>
        </thead>
        <tbody>
          {errors.map((error) => {
            const kind = errorKind(error)
            const source = [error.providerName, error.model].filter(Boolean).join(' · ') || '—'
            const covering = error.coveredBy ? ruleNames.get(error.coveredBy) : undefined
            return (
              <tr key={error.signature}>
                <td>
                  <div className={styles.errorKind}>
                    <strong>{kind.title}</strong>
                    {kind.code ? <code>{kind.code}</code> : null}
                  </div>
                </td>
                <td>
                  <span className={styles.detail}>{error.detail || <span className={styles.muted}>No detail recorded</span>}</span>
                  <span className={`${styles.source} ${styles.compactSource}`}>{source}</span>
                </td>
                <td><span className={styles.source}>{source}</span></td>
                <td>
                  <div className={styles.seen}>
                    <span>{error.count === 1 ? 'Once' : `${formatInteger(error.count)}×`}</span>
                    <small>last {formatClock(error.lastAt)}</small>
                  </div>
                </td>
                <td>
                  {error.coveredBy ? (
                    covering !== undefined
                      ? <button type="button" className={styles.covered} aria-label={`Covered by ${covering}. Edit the rule`} onClick={() => onOpenRule(error.coveredBy as string)}><CheckCircle2 size={14} aria-hidden="true" />Covered by {covering}</button>
                      : <span className={styles.covered} data-static=""><CheckCircle2 size={14} aria-hidden="true" />Covered by {error.coveredBy}</span>
                  ) : (
                    <Button type="button" variant="ghost" disabled={!canCreate} title={canCreate ? undefined : 'The retry-rule limit is reached.'} onClick={() => onCreate(error)}>
                      <Plus size={14} />Create retry rule
                    </Button>
                  )}
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}
