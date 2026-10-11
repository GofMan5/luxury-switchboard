import type { CodexAccount } from '../domain/codex'
import { useCodex } from './useCodex'
import { Button } from '../../../shared/ui/Button'
import { useModalFocus } from '../../../shared/ui/useModalFocus'
import styles from './CodexConnectPane.module.css'

/**
 * Confirmation for signing out or removing Codex accounts. Without `remove`
 * only the sessions end; with it the accounts also leave this device. The
 * dialog reads the shared account set itself, so the copy always knows how
 * many signed-in accounts survive the batch: while any are left the preset
 * keeps routing, and only the last one disables it. It renders above the Add
 * provider dialog (later in the DOM, so the scrim stacks) and cannot be left
 * while the operation is pending; a failed attempt surfaces here.
 */
export default function DisconnectCodexDialog({ accounts, remove, pending, error, onCancel, onConfirm }: {
  readonly accounts: readonly CodexAccount[]
  readonly remove: boolean
  readonly pending: boolean
  readonly error: string
  readonly onCancel: () => void
  readonly onConfirm: () => void
}) {
  const { state } = useCodex()
  const dialogRef = useModalFocus<HTMLElement>(onCancel, pending)
  if (accounts.length === 0) return null
  const ids = new Set(accounts.map((account) => account.accountId))
  const otherSignedIn = state.accounts.filter((account) => account.state === 'signed_in' && !ids.has(account.accountId)).length
  const single = accounts.length === 1
  const first = accounts[0]
  const who = single
    ? first.email !== '' ? <span className={styles.email} title={first.email}>{first.email}</span> : 'The account'
    : `${accounts.length} accounts`
  const outcome = remove ? 'signed out and removed from this device' : 'signed out'
  return (
    <div className="ui-scrim">
      <section ref={dialogRef} className={`ui-modal ${styles.disconnect}`} role="dialog" aria-modal="true" aria-label={remove ? (single ? 'Remove Codex account' : 'Remove Codex accounts') : 'Disconnect Codex'}>
        <header>
          <div>
            <h2>{remove ? (single ? 'Remove Codex account?' : 'Remove Codex accounts?') : 'Disconnect Codex?'}</h2>
            <p>
              {who} {single ? 'is' : 'are'} {outcome}.
              {otherSignedIn > 0
                ? ' The provider stays in the list and keeps working through its other signed-in accounts.'
                : ` The provider stays in the list, disabled, and model routes that used it must be reassigned. ${remove ? (single ? 'Add the account again at any time.' : 'Add the accounts again at any time.') : 'Connect again at any time.'}`}
            </p>
          </div>
        </header>
        {single ? null : (
          <ul className={styles.disconnectAccounts}>
            {accounts.map((account) => (
              <li key={`${account.accountId}:${account.state}`}>{account.email !== '' ? account.email : account.accountId}</li>
            ))}
          </ul>
        )}
        {error !== '' ? <p className={styles.disconnectError} role="alert">{error}</p> : null}
        <footer>
          <Button disabled={pending} onClick={onCancel}>Cancel</Button>
          <Button variant="danger" disabled={pending} onClick={onConfirm}>
            {pending ? (remove ? 'Removing…' : 'Disconnecting…') : remove ? 'Remove' : 'Disconnect'}
          </Button>
        </footer>
      </section>
    </div>
  )
}
