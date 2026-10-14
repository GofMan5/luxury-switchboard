import { Button } from '../../../shared/ui/Button'
import { useModalFocus } from '../../../shared/ui/useModalFocus'
import type { RouteTarget } from '../domain/route'
import styles from './ModelRoutesPage.module.css'

export function DeleteRouteDialog({ publicModel, routeCount, target, pending, error, onClose, onConfirm }: {
  readonly publicModel: string
  readonly routeCount: number
  readonly target: RouteTarget
  readonly pending: boolean
  readonly error: string
  readonly onClose: () => void
  readonly onConfirm: () => Promise<void>
}) {
  const dialogRef = useModalFocus<HTMLElement>(onClose, pending)
  const chained = target === 'relay' && routeCount > 1

  return (
    <div className="ui-scrim">
      <section ref={dialogRef} className="ui-modal" role="dialog" aria-modal="true" aria-label="Delete model route">
        <header>
          <div>
            <h2 className={styles.confirmName}>Delete “{publicModel}”?</h2>
            <p>{routeCount > 1 ? `All ${routeCount} assignments for this model are removed, not just this provider. ` : ''}{target === 'tunnel' ? 'This alias will no longer be published by the tunnel. Local relay assignments stay unchanged.' : 'The local relay will fall back to the active provider for this model. Public tunnel aliases stay unchanged.'}</p>
          </div>
        </header>
        {error ? <div className={styles.form}><p className={styles.formError} role="alert">{error}</p></div> : null}
        <footer>
          <Button type="button" disabled={pending} onClick={onClose}>Cancel</Button>
          <Button type="button" variant="danger" disabled={pending} onClick={() => void onConfirm()}>{pending ? 'Deleting…' : chained ? 'Delete chain' : 'Delete route'}</Button>
        </footer>
      </section>
    </div>
  )
}
