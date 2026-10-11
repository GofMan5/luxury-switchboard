import { useEffect, useRef, type ReactElement } from 'react'
import { LogIn, LogOut, Plus, RefreshCcw, UserRoundPlus } from 'lucide-react'
import { Button } from '../../../shared/ui/Button'
import { EmptyState, Pill } from '../../../shared/ui/chrome'
import type { CodexAccount, CodexAccountState, CodexQuotaWindow } from '../domain/codex'
import type { CodexModel, CodexQuotaCard } from '../application/codex-model'
import { useCodex } from './useCodex'
import styles from './CodexAccountsPane.module.css'

type PillTone = 'neutral' | 'success' | 'warning' | 'danger' | 'info'

type Props = {
  /** Whether this build offers the sign-in flow (codex.login capability). */
  readonly canSignIn: boolean
  /** The empty state's Add account button — opens the sign-in dialog. */
  readonly onAddAccount: () => void
  /** A row whose session is missing asks the page to start a sign-in. */
  readonly onSignIn: () => void
  /** A row's sign-out asks the page to confirm before unlinking. */
  readonly onSignOut: (account: CodexAccount) => void
}

function accountPill(state: CodexAccountState): { label: string; tone: PillTone } {
  switch (state) {
    case 'signed_in':
      return { label: 'Signed in', tone: 'success' }
    case 'reauth_needed':
      return { label: 'Sign-in needed', tone: 'warning' }
    default:
      return { label: 'Signed out', tone: 'neutral' }
  }
}

/**
 * The window's own name, derived from its length exactly like the cockpit
 * derived it — minutes decide, and a window that did not say its length falls
 * back to the constant its slot is known by. The label is a noun, never a
 * credential: it carries no account data.
 */
function windowLabel(minutes: number | undefined, fallback: string): string {
  if (minutes === undefined) return fallback
  if (minutes >= 10_079) return 'Weekly'
  if (minutes >= 1_439) return `${Math.round(minutes / 1_440)}d`
  if (minutes >= 60) return `${Math.round(minutes / 60)}h`
  return `${minutes}m`
}

/** Absolute local stamp, same-day is time-only; any other day names its weekday. */
function stamp(unix: number): string {
  const date = new Date(unix * 1_000)
  const time = date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  if (date.toDateString() === new Date().toDateString()) return time
  return `${date.toLocaleDateString([], { weekday: 'short' })} ${time}`
}

function meterTone(remaining: number): 'ok' | 'warning' | 'danger' {
  if (remaining <= 10) return 'danger'
  if (remaining <= 25) return 'warning'
  return 'ok'
}

/**
 * One usage window cell. The meter fills on what is LEFT (a full bar is a
 * healthy account), and a window the endpoint did not report never renders a
 * meter at all — a neutral bar would read as a healthy quota that nothing
 * actually vouches for. The column header names the slot; the meter's own
 * label names the window it actually measured, so neither lies for the other.
 */
function WindowCell({ title, usage }: { readonly title: string; readonly usage: CodexQuotaWindow }) {
  const tone = meterTone(usage.remainingPercent)
  return (
    <td className={styles.windowCell}>
      <div
        className={styles.meter}
        role="meter"
        aria-label={`${title}, ${usage.remainingPercent}% remaining`}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={usage.remainingPercent}
        aria-valuetext={`${usage.remainingPercent}% remaining`}
      >
        <div className={styles.meterFill} data-tone={tone} style={{ width: `${usage.remainingPercent}%` }} />
      </div>
      <div className={styles.windowLine}>
        <span className={styles.remaining} data-tone={tone}>{usage.remainingPercent}% left</span>
        <span className={styles.resets}>{usage.resetAt !== undefined ? `Resets ${stamp(usage.resetAt)}` : '—'}</span>
      </div>
    </td>
  )
}

function IconAction({ label, disabled = false, danger = false, onClick, children }: {
  readonly label: string
  readonly disabled?: boolean
  readonly danger?: boolean
  readonly onClick: () => void
  readonly children: ReactElement<{ size?: number; className?: string }>
}) {
  return (
    <button
      type="button"
      className={styles.iconAction}
      data-danger={danger}
      aria-label={label}
      title={label}
      disabled={disabled}
      onClick={onClick}
    >
      {children}
    </button>
  )
}

/**
 * One account, one row: identity, state, its two windows, when the usage was
 * measured, and the row's own actions. A probe belongs to the row it answers
 * for — a second account's refresh never touches this one, and a row that is
 * not signed in keeps its refresh locked because there is nothing to ask. The
 * sign-in action lives on the rows that need it; sign-out on every row that
 * still has a session worth unlinking.
 */
function AccountRow({ account, card, canSignIn, onSignIn, onSignOut, model }: {
  readonly account: CodexAccount
  readonly card: CodexQuotaCard | undefined
  readonly canSignIn: boolean
  readonly onSignIn: () => void
  readonly onSignOut: (account: CodexAccount) => void
  readonly model: Pick<CodexModel, 'refreshQuota'>
}) {
  const signedIn = account.state === 'signed_in'
  const pill = accountPill(account.state)
  const quota = card?.quota ?? null
  const plan = account.plan !== '' ? account.plan : quota?.planType
  const pending = card?.pending ?? false
  const showWindows = signedIn && quota !== null
  const note = !signedIn
    ? account.state === 'reauth_needed'
      ? 'The session expired. Sign in again and the usage windows return.'
      : 'This account is signed out. Sign in again to bring it back.'
    : plan

  return (
    <tr>
      <td className={styles.accountCell}>
        <div className={styles.accountLabel}>
          <strong className={styles.email} title={account.email !== '' ? account.email : undefined}>
            {account.email !== '' ? account.email : 'No Codex account'}
          </strong>
          {note ? <small className={styles.accountNote}>{note}</small> : null}
        </div>
        {signedIn && card !== undefined && card.error !== '' ? <p className={styles.rowError} role="alert">{card.error}</p> : null}
      </td>
      <td className={styles.stateCell}><Pill tone={pill.tone}>{pill.label}</Pill></td>
      {showWindows ? (
        quota.primary.present || quota.secondary.present ? (
          <>
            {quota.primary.present ? (
              <WindowCell title={`${windowLabel(quota.primary.windowMinutes, '5h')} window`} usage={quota.primary} />
            ) : (
              <td className={styles.absentCell}>—</td>
            )}
            {quota.secondary.present ? (
              <WindowCell title={`${windowLabel(quota.secondary.windowMinutes, 'Weekly')} window`} usage={quota.secondary} />
            ) : (
              <td className={styles.absentCell}>—</td>
            )}
          </>
        ) : (
          <td colSpan={2} className={styles.noteCell}>No usage windows reported for this account.</td>
        )
      ) : signedIn ? (
        <td colSpan={2} className={styles.noteCell}>{pending ? 'Checking usage…' : 'No usage loaded yet. Use Refresh usage.'}</td>
      ) : (
        <>
          <td className={styles.absentCell}>—</td>
          <td className={styles.absentCell}>—</td>
        </>
      )}
      <td className={styles.updatedCell}>{signedIn && quota ? `Updated ${stamp(quota.fetchedAt)}` : null}</td>
      <td className={styles.actionsCell}>
        <div className={styles.rowActions}>
          <IconAction
            label={`Refresh usage for ${account.email}`}
            disabled={!signedIn || pending}
            onClick={() => void model.refreshQuota(account.accountId)}
          >
            <RefreshCcw size={15} strokeWidth={1.8} aria-hidden="true" className={pending ? styles.spinning : undefined} />
          </IconAction>
          {!signedIn && canSignIn ? (
            <IconAction label={`Sign in again for ${account.email}`} onClick={onSignIn}>
              <LogIn size={15} strokeWidth={1.8} aria-hidden="true" />
            </IconAction>
          ) : null}
          {account.state !== 'signed_out' ? (
            <IconAction label={`Sign out ${account.email}`} danger onClick={() => onSignOut(account)}>
              <LogOut size={15} strokeWidth={1.8} aria-hidden="true" />
            </IconAction>
          ) : null}
        </div>
      </td>
    </tr>
  )
}

/**
 * The codex provider's answer to the keys table: every account the backend
 * holds in one dense grid, its usage windows as inline meters, and the
 * account lifecycle (sign in, refresh, sign out) on the rows themselves.
 * Probes are on demand — the pane probes each signed-in account once when it
 * appears (and retries a failed row only when the signed-in set changes,
 * never in a loop); after that the row's own button is the only trigger.
 * Nothing polls. Sign-in and sign-out are the page's to run: the pane only
 * asks, so it stays testable without a dialog.
 */
export default function CodexAccountsPane({ canSignIn, onAddAccount, onSignIn, onSignOut }: Props) {
  const { model, state } = useCodex()
  // The probe trigger is the signed-in membership itself, as a string: a
  // reorder or a re-render must not re-probe, a new account must. Cards are
  // read through the latest render rather than the deps, so a probe already
  // in flight or one that settled with usage is never asked again — and a
  // failed row is retried only when the membership changes, never in a loop.
  const latest = useRef(state)
  latest.current = state
  const signedInKey = state.accounts
    .filter((account) => account.state === 'signed_in')
    .map((account) => account.accountId)
    .join('\n')

  useEffect(() => {
    for (const account of latest.current.accounts) {
      if (account.state !== 'signed_in') continue
      const card = latest.current.quotas[account.accountId]
      if (card !== undefined && (card.pending || card.error === '')) continue
      void model.refreshQuota(account.accountId)
    }
  }, [model, signedInKey])

  if (state.accounts.length === 0) {
    return (
      <section className={styles.pane} aria-label="Codex accounts and usage">
        <div className={styles.empty}>
          <EmptyState
            icon={UserRoundPlus}
            title="No Codex accounts yet"
            hint={canSignIn
              ? 'Add a ChatGPT account and its usage windows appear here.'
              : 'Adding Codex accounts is not available in this build.'}
          />
          {canSignIn ? (
            <Button variant="primary" onClick={onAddAccount}>
              <Plus size={15} strokeWidth={1.8} aria-hidden="true" />
              Add account
            </Button>
          ) : null}
        </div>
      </section>
    )
  }

  return (
    <section className={styles.pane} aria-label="Codex accounts and usage">
      <table className={styles.table}>
        <thead>
          <tr>
            <th>Account</th>
            <th>State</th>
            <th>5h window</th>
            <th>Weekly window</th>
            <th className={styles.updatedHead}>Updated</th>
            <th>Actions</th>
          </tr>
        </thead>
        <tbody>
          {state.accounts.map((account) => (
            <AccountRow
              key={`${account.accountId}:${account.state}`}
              account={account}
              card={state.quotas[account.accountId]}
              canSignIn={canSignIn}
              onSignIn={onSignIn}
              onSignOut={onSignOut}
              model={model}
            />
          ))}
        </tbody>
      </table>
    </section>
  )
}
