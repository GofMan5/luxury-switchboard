import { useEffect, useMemo, useRef, useState, type ReactElement } from 'react'
import { LogIn, LogOut, Plus, RefreshCcw, Trash2, UserRoundPlus, Zap } from 'lucide-react'
import { Button } from '../../../shared/ui/Button'
import { EmptyState, Pill, Segmented } from '../../../shared/ui/chrome'
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
  /** A bulk removal asks the page to confirm sign-out and removal together. */
  readonly onRemove: (accounts: readonly CodexAccount[]) => void
}

/** One row's worth of derived data: the account, its settled usage card, the
 * plan badge resolved for it (the account's own text wins, the probe's plan
 * name is the fallback), and the label the row is known by. */
interface RowModel {
  readonly account: CodexAccount
  readonly card: CodexQuotaCard | undefined
  readonly plan: string
  readonly label: string
}

type StateFilter = 'all' | CodexAccountState
type SortKey = 'account' | 'state' | 'plan' | 'resets'
type ViewMode = 'table' | 'compact'

/** Deterministic text order — a plain code-unit compare, no locale to vary by host. */
function byText(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0
}

const STATE_ORDER: Record<CodexAccountState, number> = { signed_in: 0, reauth_needed: 1, signed_out: 2 }

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

function loginMethodLabel(method: string): string {
  if (method === 'device') return 'Device login'
  if (method === 'browser') return 'Browser login'
  return method
}

function countdown(totalSeconds: number): string {
  const minutes = Math.floor(totalSeconds / 60)
  if (minutes < 1) return 'under a minute'
  if (minutes < 60) return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  if (hours < 48) return `${hours}h ${minutes % 60}m`
  return `${Math.floor(hours / 24)}d ${hours % 24}h`
}

/**
 * The reset line. A reset still in the future counts down, because that is
 * the number the operator actually waits for; the exact stamp rides the
 * title for anyone who needs the moment. A past or missing reset never
 * pretends — it shows the plain stamp or nothing at all.
 */
function resetLine(resetAt: number | undefined): string {
  if (resetAt === undefined) return '—'
  const seconds = resetAt - Math.floor(Date.now() / 1_000)
  if (seconds > 0) return `Resets in ${countdown(seconds)}`
  return `Resets ${stamp(resetAt)}`
}

/** The row's checkbox label: an email when there is one, the id otherwise. */
function selectLabel(account: CodexAccount): string {
  return `Select ${account.email !== '' ? account.email : account.accountId}`
}

/**
 * One usage window's meter, shared by the table cell and the compact row.
 * The meter fills on what is LEFT (a full bar is a healthy account), and a
 * window the endpoint did not report never renders a meter at all — a
 * neutral bar would read as a healthy quota that nothing actually vouches
 * for. The column header names the slot; the meter's own label names the
 * window it actually measured, so neither lies for the other.
 */
function WindowMeter({ title, usage }: { readonly title: string; readonly usage: CodexQuotaWindow }) {
  const tone = meterTone(usage.remainingPercent)
  return (
    <>
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
        <span className={styles.resets} title={usage.resetAt !== undefined ? stamp(usage.resetAt) : undefined}>{resetLine(usage.resetAt)}</span>
      </div>
    </>
  )
}

function WindowCell({ title, usage }: { readonly title: string; readonly usage: CodexQuotaWindow }) {
  return (
    <td className={styles.windowCell}>
      <WindowMeter title={title} usage={usage} />
    </td>
  )
}

/**
 * A small square icon button with a full accessible label, used for the row
 * actions so the table stays dense without becoming unlabeled.
 */
function IconAction({ label, danger, disabled, onClick, children }: {
  readonly label: string
  readonly danger?: boolean
  readonly disabled?: boolean
  readonly onClick: () => void
  readonly children: ReactElement
}) {
  return (
    <button
      type="button"
      className={styles.iconAction}
      data-danger={danger === true ? 'true' : undefined}
      aria-label={label}
      title={label}
      disabled={disabled === true}
      onClick={onClick}
    >
      {children}
    </button>
  )
}

/** The row's lifecycle actions: refresh usage, sign in again, sign out. */
function RowActions({ account, signedIn, pending, canSignIn, onSignIn, onSignOut, model }: {
  readonly account: CodexAccount
  readonly signedIn: boolean
  readonly pending: boolean
  readonly canSignIn: boolean
  readonly onSignIn: () => void
  readonly onSignOut: (account: CodexAccount) => void
  readonly model: Pick<CodexModel, 'refreshQuota'>
}) {
  return (
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
  )
}

function AccountRow({ row, selected, waking, canSignIn, onSignIn, onSignOut, model, onToggleSelected }: {
  readonly row: RowModel
  readonly selected: boolean
  readonly waking: boolean
  readonly canSignIn: boolean
  readonly onSignIn: () => void
  readonly onSignOut: (account: CodexAccount) => void
  readonly model: Pick<CodexModel, 'refreshQuota'>
  readonly onToggleSelected: (accountId: string) => void
}) {
  const { account, card } = row
  const signedIn = account.state === 'signed_in'
  const pill = accountPill(account.state)
  const quota = card?.quota ?? null
  const pending = card?.pending ?? false
  const showWindows = signedIn && quota !== null
  const note = !signedIn
    ? account.state === 'reauth_needed'
      ? 'The session expired. Sign in again and the usage windows return.'
      : 'This account is signed out. Sign in again to bring it back.'
    : null

  return (
    <tr>
      <td className={styles.selectCell}>
        <input
          type="checkbox"
          checked={selected}
          onChange={() => onToggleSelected(account.accountId)}
          aria-label={selectLabel(account)}
        />
      </td>
      <td className={styles.accountCell}>
        <div className={styles.accountLabel}>
          <strong className={styles.email} title={account.email !== '' ? account.email : undefined}>{row.label}</strong>
          {account.loginMethod !== undefined ? <small className={styles.loginMethod}>{loginMethodLabel(account.loginMethod)}</small> : null}
          {note !== null ? <small className={styles.accountNote}>{note}</small> : null}
        </div>
        {signedIn && card !== undefined && card.error !== '' ? <p className={styles.rowError} role="alert">{card.error}</p> : null}
      </td>
      <td><div className={styles.stateCell}>
        <Pill tone={pill.tone}>{pill.label}</Pill>
        {waking ? <span className={styles.wakingNote}>Waking…</span> : null}
      </div></td>
      <td className={styles.planCell}>{row.plan !== '' ? <Pill tone="info">{row.plan}</Pill> : <span className={styles.absentValue}>—</span>}</td>
      <td className={styles.resetsCell}>{account.resets !== undefined ? account.resets : '—'}</td>
      {showWindows ? (
        <>
          {quota.primary.present ? <WindowCell title={windowLabel(quota.primary.windowMinutes, '5h window')} usage={quota.primary} /> : <td className={styles.absentCell}>—</td>}
          {quota.secondary.present ? <WindowCell title={windowLabel(quota.secondary.windowMinutes, 'Weekly window')} usage={quota.secondary} /> : <td className={styles.absentCell}>—</td>}
        </>
      ) : signedIn ? (
        <td colSpan={2} className={styles.noteCell}>{pending ? 'Checking usage…' : 'No usage loaded yet. Use Check usage.'}</td>
      ) : (
        <>
          <td className={styles.absentCell}>—</td>
          <td className={styles.absentCell}>—</td>
        </>
      )}
      <td className={styles.updatedCell}>{signedIn && quota !== null ? `Updated ${stamp(quota.fetchedAt)}` : null}</td>
      <td className={styles.actionsCell}>
        <RowActions account={account} signedIn={signedIn} pending={pending} canSignIn={canSignIn} onSignIn={onSignIn} onSignOut={onSignOut} model={model} />
      </td>
    </tr>
  )
}

function CompactRow({ row, selected, waking, canSignIn, onSignIn, onSignOut, model, onToggleSelected }: {
  readonly row: RowModel
  readonly selected: boolean
  readonly waking: boolean
  readonly canSignIn: boolean
  readonly onSignIn: () => void
  readonly onSignOut: (account: CodexAccount) => void
  readonly model: Pick<CodexModel, 'refreshQuota'>
  readonly onToggleSelected: (accountId: string) => void
}) {
  const { account, card, plan, label } = row
  const signedIn = account.state === 'signed_in'
  const pill = accountPill(account.state)
  const quota = card?.quota ?? null
  const pending = card?.pending ?? false

  return (
    <li className={styles.compactRow}>
      <div className={styles.compactHead}>
        <input
          type="checkbox"
          checked={selected}
          onChange={() => onToggleSelected(account.accountId)}
          aria-label={selectLabel(account)}
        />
        <strong className={styles.email} title={account.email !== '' ? account.email : undefined}>{label}</strong>
        {account.loginMethod !== undefined ? <small className={styles.loginMethod}>{loginMethodLabel(account.loginMethod)}</small> : null}
        <Pill tone={pill.tone}>{pill.label}</Pill>
        {plan !== '' ? <Pill tone="info">{plan}</Pill> : null}
        {account.resets !== undefined ? <span className={styles.compactResets} title="Times this session was re-provisioned">{`Resets ${account.resets}`}</span> : null}
        {waking ? <span className={styles.wakingNote}>Waking…</span> : null}
        <RowActions account={account} signedIn={signedIn} pending={pending} canSignIn={canSignIn} onSignIn={onSignIn} onSignOut={onSignOut} model={model} />
      </div>
      {!signedIn ? (
        <p className={styles.compactNote}>{account.state === 'reauth_needed' ? 'The session expired. Sign in again and the usage windows return.' : 'This account is signed out. Sign in again to bring it back.'}</p>
      ) : null}
      {signedIn && card !== undefined && card.error !== '' ? <p className={styles.rowError} role="alert">{card.error}</p> : null}
      {signedIn && quota !== null ? (
        quota.primary.present || quota.secondary.present ? (
          <div className={styles.compactWindows}>
            {quota.primary.present ? <div className={styles.compactWindow}><WindowMeter title={windowLabel(quota.primary.windowMinutes, '5h window')} usage={quota.primary} /></div> : null}
            {quota.secondary.present ? <div className={styles.compactWindow}><WindowMeter title={windowLabel(quota.secondary.windowMinutes, 'Weekly window')} usage={quota.secondary} /></div> : null}
          </div>
        ) : (
          <p className={styles.compactNote}>No usage windows reported for this account.</p>
        )
      ) : signedIn ? (
        <p className={styles.compactNote}>{pending ? 'Checking usage…' : 'No usage loaded yet. Use Check usage.'}</p>
      ) : null}
      <span className={styles.compactUpdated}>{signedIn && quota !== null ? `Updated ${stamp(quota.fetchedAt)}` : null}</span>
    </li>
  )
}

/**
 * The codex provider's answer to the keys table: every account the backend
 * holds in one dense grid, its plan, its session resets, its usage windows as
 * inline meters, and the account lifecycle (sign in, refresh, sign out) on
 * the rows themselves. The toolbar is cockpit-shaped: search, state and plan
 * filters, sort, a compact list view, and scope-follows-selection actions —
 * an empty selection means the whole fleet, a checked set means exactly those
 * accounts. Probes are on demand — the pane probes each signed-in account
 * once when it appears (and retries a failed row only when the signed-in set
 * changes, never in a loop); after that the row's own button and the
 * toolbar's Check usage are the only triggers. Nothing polls. Sign-in,
 * sign-out and removal are the page's to run: the pane only asks, so it
 * stays testable without a dialog.
 */
export default function CodexAccountsPane({ canSignIn, onAddAccount, onSignIn, onSignOut, onRemove }: Props) {
  const { model, state } = useCodex()
  const [query, setQuery] = useState('')
  const [stateFilter, setStateFilter] = useState<StateFilter>('all')
  const [planFilter, setPlanFilter] = useState('')
  const [sortKey, setSortKey] = useState<SortKey>('account')
  const [view, setView] = useState<ViewMode>('table')
  const [selected, setSelected] = useState<ReadonlySet<string>>(() => new Set<string>())

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

  // Selection may only name accounts that still exist: a removal that the
  // page just confirmed drops its rows, and the stale ids go with them.
  useEffect(() => {
    setSelected((previous) => {
      if (previous.size === 0) return previous
      const next = new Set<string>()
      for (const account of state.accounts) {
        if (previous.has(account.accountId)) next.add(account.accountId)
      }
      return next.size === previous.size ? previous : next
    })
  }, [state.accounts])

  const rows = useMemo(() => {
    const needle = query.trim().toLowerCase()
    const base: RowModel[] = state.accounts.map((account) => {
      const card = state.quotas[account.accountId]
      const plan = account.plan !== '' ? account.plan : (card?.quota?.planType ?? '')
      return { account, card, plan, label: account.email !== '' ? account.email : 'No Codex account' }
    })
    const filtered = base.filter((row) => {
      if (needle !== '' && !row.account.email.toLowerCase().includes(needle) && !row.account.accountId.toLowerCase().includes(needle)) return false
      if (stateFilter !== 'all' && row.account.state !== stateFilter) return false
      if (planFilter !== '' && row.plan !== planFilter) return false
      return true
    })
    const sorted = [...filtered]
    sorted.sort((a, b) => {
      switch (sortKey) {
        case 'state':
          return (STATE_ORDER[a.account.state] - STATE_ORDER[b.account.state]) || byText(a.label, b.label)
        case 'plan':
          return byText(a.plan, b.plan) || byText(a.label, b.label)
        case 'resets':
          return ((b.account.resets ?? -1) - (a.account.resets ?? -1)) || byText(a.label, b.label)
        default:
          return byText(a.account.accountId, b.account.accountId) || byText(a.label, b.label)
      }
    })
    return sorted
  }, [state.accounts, state.quotas, query, stateFilter, planFilter, sortKey])

  const plans = useMemo(() => {
    const set = new Set<string>()
    for (const account of state.accounts) {
      const plan = account.plan !== '' ? account.plan : (state.quotas[account.accountId]?.quota?.planType ?? '')
      if (plan !== '') set.add(plan)
    }
    return [...set].sort((a, b) => byText(a, b))
  }, [state.accounts, state.quotas])

  const selectedAccounts = state.accounts.filter((account) => selected.has(account.accountId))
  const scopeIds = selectedAccounts.map((account) => account.accountId)
  const checkTargets = selectedAccounts.filter((account) => account.state === 'signed_in')
  const waking = state.wakingIds.length > 0
  const checkBusy = state.probingAll || checkTargets.some((account) => state.quotas[account.accountId]?.pending === true)
  const canCheck = scopeIds.length > 0 ? checkTargets.length > 0 : state.accounts.some((account) => account.state === 'signed_in')

  const allShown = rows.length > 0 && rows.every((row) => selected.has(row.account.accountId))
  const someShown = rows.some((row) => selected.has(row.account.accountId))
  const selectAllRef = (element: HTMLInputElement | null) => {
    if (element !== null) element.indeterminate = someShown && !allShown
  }
  const toggleAll = () => {
    setSelected((previous) => {
      const next = new Set(previous)
      for (const row of rows) {
        if (allShown) next.delete(row.account.accountId)
        else next.add(row.account.accountId)
      }
      return next
    })
  }
  const toggleRow = (accountId: string) => {
    setSelected((previous) => {
      const next = new Set(previous)
      if (next.has(accountId)) next.delete(accountId)
      else next.add(accountId)
      return next
    })
  }

  const wakeAccounts = () => {
    void model.wake(scopeIds.length > 0 ? scopeIds : null)
  }
  const checkUsage = () => {
    if (scopeIds.length > 0) {
      for (const account of checkTargets) void model.refreshQuota(account.accountId)
      return
    }
    void model.refreshAll()
  }

  if (state.accounts.length === 0) {
    return (
      <section className={styles.pane} aria-label="Codex accounts and usage">
        <div className={styles.empty}>
          <EmptyState
            icon={UserRoundPlus}
            title="No Codex accounts yet"
            hint={canSignIn ? 'Add a ChatGPT account and its usage windows appear here.' : 'Adding Codex accounts is not available in this build.'}
          />
          {canSignIn ? (
            <Button variant="primary" onClick={onAddAccount}>
              <Plus size={16} aria-hidden="true" />Add account
            </Button>
          ) : null}
        </div>
      </section>
    )
  }

  const sharedRows = {
    canSignIn,
    onSignIn,
    onSignOut,
    model,
    onToggleSelected: toggleRow,
  }

  return (
    <section className={styles.pane} aria-label="Codex accounts and usage">
      <div className={styles.toolbar}>
        <div className={styles.toolbarFilters}>
          <input
            type="search"
            className={styles.search}
            placeholder="Search by email or ID"
            aria-label="Search accounts"
            value={query}
            onChange={(event) => setQuery(event.currentTarget.value)}
          />
          <label className={styles.toolbarSelect}>
            <span>State</span>
            <select value={stateFilter} onChange={(event) => setStateFilter(event.currentTarget.value as StateFilter)}>
              <option value="all">All states</option>
              <option value="signed_in">Signed in</option>
              <option value="reauth_needed">Sign-in needed</option>
              <option value="signed_out">Signed out</option>
            </select>
          </label>
          <label className={styles.toolbarSelect}>
            <span>Plan</span>
            <select value={planFilter} onChange={(event) => setPlanFilter(event.currentTarget.value)}>
              <option value="">All plans</option>
              {plans.map((plan) => <option key={plan} value={plan}>{plan}</option>)}
            </select>
          </label>
          <label className={styles.toolbarSelect}>
            <span>Sort</span>
            <select value={sortKey} onChange={(event) => setSortKey(event.currentTarget.value as SortKey)}>
              <option value="account">Account</option>
              <option value="state">State</option>
              <option value="plan">Plan</option>
              <option value="resets">Resets</option>
            </select>
          </label>
          <Segmented
            options={[{ id: 'table', label: 'Table' }, { id: 'compact', label: 'List' }]}
            value={view}
            onChange={setView}
            label="Layout"
          />
        </div>
        <div className={styles.toolbarActions}>
          <Button
            disabled={waking}
            onClick={wakeAccounts}
            title={scopeIds.length > 0 ? 'Refresh the sessions of the selected accounts' : 'Refresh every account session'}
          >
            <Zap size={16} aria-hidden="true" className={waking ? styles.spinning : undefined} />
            {waking ? 'Waking…' : scopeIds.length > 0 ? `Wake ${scopeIds.length}` : 'Wake all'}
          </Button>
          <Button
            disabled={!canCheck || state.probingAll || checkBusy}
            onClick={checkUsage}
            title={scopeIds.length > 0 ? 'Fetch usage for the selected accounts' : 'Fetch usage for every account'}
          >
            <RefreshCcw size={16} aria-hidden="true" className={checkBusy ? styles.spinning : undefined} />
            {checkBusy ? 'Checking…' : scopeIds.length > 0 ? `Check ${checkTargets.length}` : 'Check usage'}
          </Button>
          {scopeIds.length > 0 ? (
            <Button
              variant="danger"
              onClick={() => onRemove(selectedAccounts)}
              title="Sign the selected accounts out and remove them from this device"
            >
              <Trash2 size={16} aria-hidden="true" />{`Remove ${scopeIds.length}`}
            </Button>
          ) : null}
          {scopeIds.length > 0 ? (
            <Button variant="ghost" onClick={() => setSelected(new Set<string>())}>Clear selection</Button>
          ) : null}
        </div>
      </div>
      {state.wakeError !== '' ? <p className={styles.paneError} role="alert">{state.wakeError}</p> : null}
      {view === 'table' ? (
        <div className={styles.tableWrap}>
          <table className={styles.table}>
          <thead>
            <tr>
              <th className={styles.selectHead}>
                <input
                  type="checkbox"
                  ref={selectAllRef}
                  checked={allShown}
                  onChange={toggleAll}
                  aria-label="Select all accounts"
                />
              </th>
              <th aria-sort={sortKey === 'account' ? 'ascending' : undefined}>Account</th>
              <th aria-sort={sortKey === 'state' ? 'ascending' : undefined}>State</th>
              <th aria-sort={sortKey === 'plan' ? 'ascending' : undefined}>Plan</th>
              <th aria-sort={sortKey === 'resets' ? 'descending' : undefined}>Resets</th>
              <th>5h window</th>
              <th>Weekly window</th>
              <th className={styles.updatedHead}>Updated</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            {rows.length === 0 ? (
              <tr>
                <td colSpan={9} className={styles.noteCell}>No accounts match the current search and filters.</td>
              </tr>
            ) : (
              rows.map((row) => (
                <AccountRow
                  key={`${row.account.accountId}:${row.account.state}`}
                  row={row}
                  selected={selected.has(row.account.accountId)}
                  waking={state.wakingIds.includes(row.account.accountId)}
                  {...sharedRows}
                />
              ))
            )}
          </tbody>
          </table>
        </div>
      ) : (
        <ul className={styles.compactList} aria-label="Codex accounts and usage">
          {rows.length === 0 ? (
            <li className={styles.noMatch}>No accounts match the current search and filters.</li>
          ) : (
            rows.map((row) => (
              <CompactRow
                key={`${row.account.accountId}:${row.account.state}`}
                row={row}
                selected={selected.has(row.account.accountId)}
                waking={state.wakingIds.includes(row.account.accountId)}
                {...sharedRows}
              />
            ))
          )}
        </ul>
      )}
    </section>
  )
}
