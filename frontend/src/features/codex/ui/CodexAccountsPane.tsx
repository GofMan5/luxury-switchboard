import { useEffect } from 'react'
import { RefreshCcw } from 'lucide-react'
import { Button } from '../../../shared/ui/Button'
import { Pill } from '../../../shared/ui/chrome'
import type { CodexAccountState, CodexQuotaWindow } from '../domain/codex'
import { useCodex } from './useCodex'
import styles from './CodexAccountsPane.module.css'

type PillTone = 'neutral' | 'success' | 'warning' | 'danger' | 'info'

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
 * One usage window row of the accounts list. The meter fills on what is LEFT
 * (a full bar is a healthy account), and a window the endpoint did not report
 * never renders a row at all — a neutral bar would read as a healthy quota
 * that nothing actually vouches for.
 */
function WindowRow({ title, usage }: { readonly title: string; readonly usage: CodexQuotaWindow }) {
  const tone = meterTone(usage.remainingPercent)
  return (
    <li className={styles.window}>
      <strong className={styles.windowTitle}>{title}</strong>
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
      <span className={styles.remaining} data-tone={tone}>{usage.remainingPercent}% left</span>
      <span className={styles.resets}>{usage.resetAt !== undefined ? `Resets ${stamp(usage.resetAt)}` : '—'}</span>
    </li>
  )
}

/**
 * The codex provider's answer to the keys list: the account it answers for,
 * plus its usage windows as list rows. The backend holds exactly one signed-in
 * Codex account, so the list is one account row — the shape stays a list, and
 * a second account would render as a second row, not a redesign. Probes are
 * on demand (mount, account-state change, this pane's button); nothing polls.
 */
export default function CodexAccountsPane() {
  const { model, state } = useCodex()
  const { account, quota, quotaPending, quotaError } = state
  const signedIn = account.state === 'signed_in'

  // Mount and every account-state change: one probe per live session. Sign-in
  // flips the state and lands here with the fresh account already in place.
  useEffect(() => {
    if (signedIn) void model.refreshQuota()
  }, [model, signedIn])

  const pill = accountPill(account.state)
  const plan = account.plan !== '' ? account.plan : quota?.planType
  // The slot a window falls into is known by its length; a window the probe
  // did not report never renders a row, so the titles below never lie.
  const rows: readonly { id: 'primary' | 'secondary'; title: string; usage: CodexQuotaWindow | undefined }[] = [
    { id: 'primary', title: `${windowLabel(quota?.primary.windowMinutes, '5h')} window`, usage: quota?.primary },
    { id: 'secondary', title: `${windowLabel(quota?.secondary.windowMinutes, 'Weekly')} window`, usage: quota?.secondary },
  ]
  const windows = rows.filter((row): row is { id: 'primary' | 'secondary'; title: string; usage: CodexQuotaWindow } => row.usage !== undefined && row.usage.present)

  return (
    <section className={styles.pane} aria-label="Codex account and usage">
      <div className={styles.account}>
        <div className={styles.accountText}>
          <span className={styles.email} title={account.email}>{account.email !== '' ? account.email : 'No Codex account'}</span>
          <small>ChatGPT account this provider answers for</small>
        </div>
        {plan ? <Pill tone="info">{plan}</Pill> : null}
        <Pill tone={pill.tone}>{pill.label}</Pill>
        <Button onClick={() => void model.refreshQuota()} disabled={!signedIn || quotaPending}>
          <RefreshCcw size={16} aria-hidden="true" className={quotaPending ? styles.spinning : undefined} />
          {quotaPending ? 'Checking…' : 'Refresh usage'}
        </Button>
      </div>

      {signedIn && quotaError !== '' ? <p className={styles.error} role="alert">{quotaError}</p> : null}

      {signedIn ? (
        quota ? (
          <>
            {windows.length > 0 ? (
              <ul className={styles.windows}>
                {windows.map((row) => <WindowRow key={row.id} title={row.title} usage={row.usage} />)}
              </ul>
            ) : (
              <p className={styles.hint}>No usage windows reported for this account.</p>
            )}
            <p className={styles.stamp}>Updated {stamp(quota.fetchedAt)}</p>
          </>
        ) : quotaPending ? (
          <p className={styles.hint}>Checking usage…</p>
        ) : (
          <p className={styles.hint}>No usage loaded yet. Use Refresh usage.</p>
        )
      ) : (
        <p className={styles.hint}>
          {account.state === 'reauth_needed'
            ? 'The session expired. Sign in again from the Providers page and the usage windows return.'
            : 'Codex is not signed in. Sign in from the Providers page and the usage windows appear here.'}
        </p>
      )}
    </section>
  )
}
