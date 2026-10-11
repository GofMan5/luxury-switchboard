import type { ControlPlaneSession } from '../../../platform/stdio/session'
import { openExternal } from '../../../platform/lifecycle/open-external'
import { ControlPlaneError } from '../../../shared/contracts/protocol'
import { aggregateCodexState, type CodexAccount, type CodexAccountState, type CodexLoginPhase, type CodexLoginStatus, type CodexQuotaReport, type CodexQuotaResult, type CodexQuotaWindow, type CodexStatus, type CodexWakeOutcome, type CodexWakeResult } from '../domain/codex'
import type { CodexDeviceLoginStart, CodexImportResult, CodexPort, CodexQuotaAllResult } from '../application/codex-port'

const LOGIN_PHASES: readonly CodexLoginPhase[] = ['idle', 'waiting', 'exchanging', 'success', 'error']
const ACCOUNT_STATES: readonly CodexAccountState[] = ['signed_out', 'signed_in', 'reauth_needed']

type AccountRow = { state?: string; email?: unknown; plan?: unknown; accountId?: unknown; providerId?: unknown; loginMethod?: unknown; resets?: unknown }
type StatusPayload = { state?: string; accounts?: unknown; importedFrom?: unknown }
type QuotaPayload = { accountId?: unknown; quota?: unknown; error?: unknown }
type QuotaAllPayload = { results?: unknown }
type QuotaReportPayload = { fetchedAt?: unknown; planType?: unknown; primary?: unknown; secondary?: unknown }
type QuotaWindowPayload = { present?: unknown; remainingPercent?: unknown; windowMinutes?: unknown; resetAt?: unknown }
type WakePayload = { results?: unknown }

/** The window a probe did not report: no meter, neutral full value. */
const unreportedWindow: CodexQuotaWindow = { present: false, remainingPercent: 100 }

export class StdioCodexPort implements CodexPort {
  readonly #session: ControlPlaneSession
  readonly #openUrl: (url: string) => Promise<void>

  constructor(session: ControlPlaneSession, openUrl: (url: string) => Promise<void> = openExternal) {
    this.#session = session
    this.#openUrl = openUrl
  }

  loginStart(signal?: AbortSignal): Promise<{ authorizeUrl: string }> {
    const result = this.#session.call<{ authorizeUrl?: string }>('codex.login.start', undefined, signal)
    return result.then((value) => ({ authorizeUrl: value.authorizeUrl ?? '' }))
  }

  loginStatus(signal?: AbortSignal): Promise<CodexLoginStatus> {
    const result = this.#session.call<{ phase?: string; error?: unknown; deviceUserCode?: unknown; deviceVerificationUrl?: unknown }>('codex.login.status', undefined, signal)
    return result.then((value) => {
      const phase = LOGIN_PHASES.find((candidate) => candidate === value.phase)
      return {
        phase: phase ?? 'idle',
        error: typeof value.error === 'string' ? value.error : undefined,
        // Device fields ride the same answer; absent or malformed means no
        // device flow is live, which is the state the model falls back to.
        deviceUserCode: typeof value.deviceUserCode === 'string' ? value.deviceUserCode : undefined,
        deviceVerificationUrl: typeof value.deviceVerificationUrl === 'string' ? value.deviceVerificationUrl : undefined,
      }
    })
  }

  async loginCancel(signal?: AbortSignal): Promise<void> {
    await this.#session.call('codex.login.cancel', undefined, signal)
  }

  deviceLoginStart(signal?: AbortSignal): Promise<CodexDeviceLoginStart> {
    const result = this.#session.call<{ userCode?: unknown; verificationUrl?: unknown; pollIntervalSeconds?: unknown }>('codex.login.device.start', undefined, signal)
    return result.then((value) => ({
      userCode: typeof value.userCode === 'string' ? value.userCode : '',
      verificationUrl: typeof value.verificationUrl === 'string' ? value.verificationUrl : '',
      pollIntervalSeconds: typeof value.pollIntervalSeconds === 'number' && Number.isFinite(value.pollIntervalSeconds) && value.pollIntervalSeconds >= 0 ? value.pollIntervalSeconds : 0,
    }))
  }

  importJson(text: string, signal?: AbortSignal): Promise<CodexImportResult> {
    const result = this.#session.call<StatusPayload>('codex.import.json', { text }, signal)
    return result.then(coerceImportResult)
  }

  importFiles(paths: readonly string[], signal?: AbortSignal): Promise<CodexImportResult> {
    const result = this.#session.call<StatusPayload>('codex.import.files', { paths: [...paths] }, signal)
    return result.then(coerceImportResult)
  }

  status(signal?: AbortSignal): Promise<CodexStatus> {
    const result = this.#session.call<StatusPayload>('codex.status', undefined, signal)
    return result.then((value) => {
      if (!Array.isArray(value.accounts)) return { state: 'signed_out', accounts: [] }
      const accounts = coerceAccountRows(value.accounts)
      // The aggregate state is a function of the rows: a payload whose state
      // field is broken still reports real rows, so the state is recomputed
      // from them rather than wiping the accounts away.
      const stated = ACCOUNT_STATES.find((candidate) => candidate === value.state)
      return { state: stated ?? aggregateCodexState(accounts), accounts }
    })
  }

  quota(accountId: string, signal?: AbortSignal): Promise<CodexQuotaResult> {
    const result = this.#session.call<QuotaPayload>('codex.quota', { accountId }, signal)
    return result.then(coerceQuotaResult)
  }

  quotaAll(signal?: AbortSignal): Promise<CodexQuotaAllResult> {
    const result = this.#session.call<QuotaAllPayload>('codex.quota.all', undefined, signal)
    // The fleet check answers the same per-account rows the single probe
    // answers; a malformed list is "no rows", never a crash.
    return result.then((value) => ({ results: Array.isArray(value.results) ? value.results.flatMap(coerceQuotaRows) : [] }))
  }

  async wake(accountIds: readonly string[] | null, signal?: AbortSignal): Promise<CodexWakeResult> {
    // null asks the backend for the whole fleet in one call; the wire carries
    // a single optional accountId, so a named selection becomes one call per
    // account and the rows merge in the selection's order. An empty
    // selection wakes nothing, and waking nothing is a completed wake.
    if (accountIds !== null && accountIds.length === 0) return { results: [] }
    if (accountIds === null) {
      const value = await this.#session.call<WakePayload>('codex.wake', undefined, signal)
      return { results: coerceWakeRows(value) }
    }
    const answers = await Promise.all(accountIds.map((accountId) =>
      this.#session.call<WakePayload>('codex.wake', { accountId }, signal)))
    return { results: answers.flatMap((value) => coerceWakeRows(value)) }
  }

  async logout(accountId: string | null, remove: boolean, signal?: AbortSignal): Promise<void> {
    // The frame carries only what this logout asks for: a target account
    // when one is named, the remove flag when the cascade is wanted. An
    // all-accounts disconnect stays the empty payload the backend already
    // understood.
    const payload: { accountId?: string; remove?: boolean } = {}
    if (accountId !== null) payload.accountId = accountId
    if (remove) payload.remove = true
    await this.#session.call('codex.logout', Object.keys(payload).length > 0 ? payload : undefined, signal)
  }

  async openAuthorizeUrl(url: string): Promise<void> {
    let https = false
    try {
      https = new URL(url).protocol === 'https:'
    } catch {
      https = false
    }
    if (!https) throw new ControlPlaneError('insecure_url', 'The sign-in address was not HTTPS.')
    await this.#openUrl(url)
  }

  subscribe(listener: () => void): () => void {
    return this.#session.subscribe('codex.changed', listener)
  }
}

/** An import answer is the codex.status shape; a malformed one degrades to
 * signed-out with no rows, which the model reads as a failed import. */
function coerceImportResult(value: StatusPayload): CodexImportResult {
  if (!Array.isArray(value.accounts)) return { state: 'signed_out', accounts: [] }
  const accounts = coerceAccountRows(value.accounts)
  const stated = ACCOUNT_STATES.find((candidate) => candidate === value.state)
  return {
    state: stated ?? aggregateCodexState(accounts),
    accounts,
    importedFrom: typeof value.importedFrom === 'string' && value.importedFrom !== '' ? value.importedFrom : undefined,
  }
}

/** Rows survive with broken fields emptied out; an entry that is not an
 * object is not an account and is dropped, never coerced into a phantom. */
function coerceAccountRows(entries: readonly unknown[]): readonly CodexAccount[] {
  return entries.flatMap((entry): CodexAccount[] => {
    if (typeof entry !== 'object' || entry === null) return []
    const row = entry as AccountRow
    const state = ACCOUNT_STATES.find((candidate) => candidate === row.state)
    // loginMethod and resets ride the row whenever the backend's store
    // carries them; anything malformed reads as "not reported" rather than
    // a guess, exactly like the label fields above.
    const resets = row.resets
    return [{
      state: state ?? 'signed_out',
      email: typeof row.email === 'string' ? row.email : '',
      plan: typeof row.plan === 'string' ? row.plan : '',
      accountId: typeof row.accountId === 'string' ? row.accountId : '',
      providerId: typeof row.providerId === 'string' ? row.providerId : '',
      loginMethod: typeof row.loginMethod === 'string' && row.loginMethod !== '' ? row.loginMethod : undefined,
      resets: typeof resets === 'number' && Number.isFinite(resets) && Number.isInteger(resets) && resets >= 0 ? resets : undefined,
    }]
  })
}

/** A quota block only exists once a probe succeeded (fetchedAt is set); a
 * malformed or missing block means "no settled probe yet", never a guess. */
function coerceQuotaReport(value: unknown): CodexQuotaReport | undefined {
  if (typeof value !== 'object' || value === null) return undefined
  const report = value as QuotaReportPayload
  const fetchedAt = report.fetchedAt
  if (typeof fetchedAt !== 'number' || !Number.isFinite(fetchedAt) || fetchedAt <= 0) return undefined
  return {
    fetchedAt,
    planType: typeof report.planType === 'string' && report.planType !== '' ? report.planType : undefined,
    primary: coerceQuotaWindow(report.primary),
    secondary: coerceQuotaWindow(report.secondary),
  }
}

/** A window is only drawable when the endpoint actually reported one and the
 * meter value survived the wire; anything else reads as not reported, so a
 * broken shell cannot paint a meter that lies in either direction. */
function coerceQuotaWindow(value: unknown): CodexQuotaWindow {
  if (typeof value !== 'object' || value === null) return unreportedWindow
  const window = value as QuotaWindowPayload
  if (window.present !== true) return unreportedWindow
  const remaining = window.remainingPercent
  if (typeof remaining !== 'number' || !Number.isFinite(remaining)) return unreportedWindow
  const minutes = window.windowMinutes
  const resetAt = window.resetAt
  return {
    present: true,
    remainingPercent: Math.min(100, Math.max(0, Math.round(remaining))),
    windowMinutes: typeof minutes === 'number' && Number.isFinite(minutes) && minutes >= 1 ? Math.round(minutes) : undefined,
    resetAt: typeof resetAt === 'number' && Number.isFinite(resetAt) && resetAt > 0 ? resetAt : undefined,
  }
}

/** One quota row: the account, the settled probe, and a failure as status
 * text. A probe failure is a result field, never a transport rejection;
 * anything but text has nothing to say and stays silent. */
function coerceQuotaResult(entry: unknown): CodexQuotaResult {
  if (typeof entry !== 'object' || entry === null) return { accountId: '' }
  const row = entry as QuotaPayload
  return {
    accountId: typeof row.accountId === 'string' ? row.accountId : '',
    quota: coerceQuotaReport(row.quota),
    error: typeof row.error === 'string' && row.error.trim() !== '' ? row.error : undefined,
  }
}

/** Quota rows for the fleet check: an entry that is not an object is not a
 * row and is dropped, never coerced into a phantom account. */
function coerceQuotaRows(entry: unknown): CodexQuotaResult[] {
  if (typeof entry !== 'object' || entry === null) return []
  return [coerceQuotaResult(entry)]
}

/** Wake rows: the account, the state the backend saw, and a failure as
 * status text when the wake could not run. An entry that is not an object
 * is dropped; an unknown state fails safe to signed_out, the same fallback
 * the status rows use. */
function coerceWakeRows(value: WakePayload): readonly CodexWakeOutcome[] {
  if (!Array.isArray(value.results)) return []
  return value.results.flatMap((entry): CodexWakeOutcome[] => {
    if (typeof entry !== 'object' || entry === null) return []
    const row = entry as { accountId?: unknown; state?: string; error?: unknown }
    const state = ACCOUNT_STATES.find((candidate) => candidate === row.state)
    return [{
      accountId: typeof row.accountId === 'string' ? row.accountId : '',
      state: state ?? 'signed_out',
      error: typeof row.error === 'string' && row.error.trim() !== '' ? row.error : undefined,
    }]
  })
}
