import { useEffect, useRef } from 'react'
import { X } from 'lucide-react'
import { Button } from '../../../shared/ui/Button'
import { useModalFocus } from '../../../shared/ui/useModalFocus'
import CodexConnectPane from './CodexConnectPane'
import { useCodex } from './useCodex'
import styles from './CodexSignInDialog.module.css'

/**
 * The codex sign-in flow on its own, outside the Add provider chooser — the
 * accounts table opens it directly for adding an account or renewing an
 * expired session. The lifecycle contract is the chooser's: leaving cancels
 * anything in flight, the outcome banner is consumed on the way out, and the
 * exchange phase blocks every exit. The host page keeps the dialogs for
 * disconnecting an account, so the pane's own Disconnect button comes back to
 * it through onDisconnect.
 */
export default function CodexSignInDialog({
  onDismiss,
  onDisconnect,
}: {
  readonly onDismiss: () => void
  readonly onDisconnect: (accountId: string) => void
}) {
  const { model, state } = useCodex()
  const { loginPhase } = state
  const blocked = loginPhase === 'exchanging'
  const live = loginPhase === 'connecting' || loginPhase === 'waiting'

  /** Leaving the flow cancels anything in flight and consumes the outcome shown. */
  const requestDismiss = () => {
    if (live) void model.cancelLogin()
    model.acknowledgeOutcome()
    onDismiss()
  }

  const dialogRef = useModalFocus<HTMLDivElement>(requestDismiss, blocked)
  // useModalFocus autofocuses exactly once on mount. Phase swaps move focus
  // back to the dialog so the Tab trap never leaks onto the page.
  const mounted = useRef(false)
  useEffect(() => {
    if (!mounted.current) {
      mounted.current = true
      return
    }
    dialogRef.current?.focus()
  }, [loginPhase, dialogRef])

  return (
    <div
      className="ui-scrim"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget && !blocked) requestDismiss()
      }}
    >
      <section
        ref={dialogRef}
        tabIndex={-1}
        className="ui-modal"
        role="dialog"
        aria-modal="true"
        aria-label="Sign in to Codex"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header>
          <div>
            <h2>Sign in to Codex</h2>
            <p>Add an account or renew an expired session.</p>
          </div>
          <button type="button" aria-label="Close" disabled={blocked} onClick={requestDismiss}>
            <X size={18} aria-hidden="true" />
          </button>
        </header>

        <div className={styles.body}>
          <CodexConnectPane
            providerExists={true}
            onSelectCodexRow={() => onDismiss()}
            onDisconnect={onDisconnect}
          />
        </div>

        <footer>
          <Button disabled={blocked} onClick={requestDismiss}>
            Cancel
          </Button>
          {loginPhase === 'success' ? (
            <Button
              variant="primary"
              onClick={() => {
                model.acknowledgeOutcome()
                onDismiss()
              }}
            >
              Done
            </Button>
          ) : null}
        </footer>
      </section>
    </div>
  )
}
