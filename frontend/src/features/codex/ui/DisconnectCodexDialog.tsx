import { Button } from '../../../shared/ui/Button'
import { useModalFocus } from '../../../shared/ui/useModalFocus'
import styles from './CodexConnectPane.module.css'

/**
 * Confirmation for disconnecting one Codex account. It renders above the Add
 * provider dialog (later in the DOM, so the scrim stacks) and cannot be left
 * while a disconnect is pending; a failed attempt surfaces here. With more
 * signed-in accounts left behind the copy narrows to the one account, because
 * the provider itself keeps working — only the last account disables it.
 */
export default function DisconnectCodexDialog({ email, otherSignedIn, pending, error, onCancel, onConfirm }: {
  readonly email: string
  readonly otherSignedIn: number
  readonly pending: boolean
  readonly error: string
  readonly onCancel: () => void
  readonly onConfirm: () => void
}) {
  const dialogRef = useModalFocus<HTMLElement>(onCancel, pending)
  const who = email !== '' ? <span className={styles.email} title={email}>{email}</span> : 'The account'
  return (
    <div className="ui-scrim">
      <section ref={dialogRef} className={`ui-modal ${styles.disconnect}`} role="dialog" aria-modal="true" aria-label="Disconnect Codex">
        <header>
          <div>
            <h2>Disconnect Codex?</h2>
            <p>
              {who} is signed out.
              {otherSignedIn > 0
                ? ' The provider stays in the list and keeps working through its other signed-in accounts.'
                : ' The provider stays in the list, disabled, and model routes that used it must be reassigned. Connect again at any time.'}
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
