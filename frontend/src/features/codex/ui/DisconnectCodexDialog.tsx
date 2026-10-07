import { Button } from '../../../shared/ui/Button'
import { useModalFocus } from '../../../shared/ui/useModalFocus'
import styles from './CodexConnectPane.module.css'

/**
 * Confirmation for disconnecting the managed Codex provider. It renders above
 * the Add provider dialog (later in the DOM, so the scrim stacks) and cannot
 * be left while a disconnect is pending; a failed attempt surfaces here.
 */
export default function DisconnectCodexDialog({ email, pending, error, onCancel, onConfirm }: {
  readonly email: string
  readonly pending: boolean
  readonly error: string
  readonly onCancel: () => void
  readonly onConfirm: () => void
}) {
  const dialogRef = useModalFocus<HTMLElement>(onCancel, pending)
  return (
    <div className="ui-scrim">
      <section ref={dialogRef} className={`ui-modal ${styles.disconnect}`} role="dialog" aria-modal="true" aria-label="Disconnect Codex">
        <header>
          <div>
            <h2>Disconnect Codex?</h2>
            <p>
              The managed Codex provider is removed{email !== '' ? <> and <span className={styles.email} title={email}>{email}</span> is signed out</> : null}.
              Model routes that used it must be reassigned. You can connect again at any time.
            </p>
          </div>
        </header>
        {error !== '' ? <p className={styles.disconnectError} role="alert">{error}</p> : null}
        <footer>
          <Button disabled={pending} onClick={onCancel}>Cancel</Button>
          <Button variant="danger" disabled={pending} onClick={onConfirm}>{pending ? 'Disconnecting…' : 'Disconnect'}</Button>
        </footer>
      </section>
    </div>
  )
}
