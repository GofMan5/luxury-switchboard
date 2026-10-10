import { useEffect, useRef } from 'react'
import { ArrowLeft, ListChecks, SlidersHorizontal, X } from 'lucide-react'
import { Button } from '../../../shared/ui/Button'
import { useModalFocus } from '../../../shared/ui/useModalFocus'
import CodexConnectPane from '../../codex/ui/CodexConnectPane'
import { useCodex } from '../../codex/ui/useCodex'
import styles from './AddProviderDialog.module.css'

/**
 * The Add provider flow. Step "choose" picks between a curated preset and a
 * hand-built provider; step "codex" runs the Codex preset sign-in. The
 * capability check happens before this dialog opens, so Custom is the only
 * offered path when the backend predates presets.
 */
export default function AddProviderDialog({
  step,
  codexProviderId,
  onDismiss,
  onBack,
  onChoosePreset,
  onCustom,
  onSelectCodexProvider,
  onDisconnect,
}: {
  readonly step: 'choose' | 'codex'
  readonly codexProviderId: string | null
  readonly onDismiss: () => void
  readonly onBack: () => void
  readonly onChoosePreset: () => void
  readonly onCustom: () => void
  readonly onSelectCodexProvider: () => void
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
  // useModalFocus autofocuses exactly once on mount. Step and phase swaps move
  // focus back to the dialog so the Tab trap never leaks onto the page — a
  // focused control can also become disabled mid-flow (Back during exchange).
  const mounted = useRef(false)
  useEffect(() => {
    if (!mounted.current) {
      mounted.current = true
      return
    }
    dialogRef.current?.focus()
  }, [step, loginPhase, dialogRef])

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
        aria-label="Add provider"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header>
          <div>
            <h2>Add provider</h2>
            <p>
              {step === 'choose'
                ? 'Start from a preset, or configure everything by hand.'
                : 'Presets configure the endpoint. The account is yours.'}
            </p>
          </div>
          <button type="button" aria-label="Close" disabled={blocked} onClick={requestDismiss}>
            <X size={18} aria-hidden="true" />
          </button>
        </header>

        <div className={styles.body}>
          {step === 'choose' ? (
            <>
              <button
                type="button"
                className={styles.optionCard}
                data-autofocus
                disabled={blocked}
                onClick={() => {
                  // A live Codex sign-in must not keep completing behind the
                  // chooser: switching cancels it first, like every other exit.
                  if (live) void model.cancelLogin()
                  model.acknowledgeOutcome()
                  onChoosePreset()
                }}
              >
                <ListChecks size={20} strokeWidth={1.6} aria-hidden="true" />
                <span className={styles.optionText}>
                  <strong>From list</strong>
                  <small>Curated presets that configure the endpoint for you. Currently one: Codex.</small>
                </span>
              </button>
              <button
                type="button"
                className={styles.optionCard}
                disabled={blocked}
                onClick={() => {
                  // Same contract as the preset card: no sign-in left running
                  // behind a switch the user made.
                  if (live) void model.cancelLogin()
                  model.acknowledgeOutcome()
                  onCustom()
                }}
              >
                <SlidersHorizontal size={20} strokeWidth={1.6} aria-hidden="true" />
                <span className={styles.optionText}>
                  <strong>Custom</strong>
                  <small>Enter the endpoint, limits and authentication yourself.</small>
                </span>
              </button>
              {blocked ? (
                // The codex step shows its own status for this phase; the
                // chooser must say it by itself, or every control here is
                // disabled for no visible reason. Byte-identical to the pane's
                // line so the two steps never disagree about the same wait.
                <p className={styles.blockedNote} role="status">Finishing sign-in…</p>
              ) : null}
            </>
          ) : (
            <>
              <Button
                variant="ghost"
                data-autofocus
                disabled={blocked}
                onClick={() => {
                  if (live) void model.cancelLogin()
                  onBack()
                }}
              >
                <ArrowLeft size={15} aria-hidden="true" />
                Back
              </Button>
              <CodexConnectPane
                providerExists={codexProviderId !== null}
                onSelectCodexRow={onSelectCodexProvider}
                onDisconnect={onDisconnect}
              />
            </>
          )}
        </div>

        <footer>
          <Button disabled={blocked} onClick={requestDismiss}>
            Cancel
          </Button>
          {step === 'codex' && loginPhase === 'success' ? (
            <Button
              variant="primary"
              onClick={() => {
                model.acknowledgeOutcome()
                onSelectCodexProvider()
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
