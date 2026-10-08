import type { ControlPlaneSession } from '../../../platform/stdio/session'
import { openExternal } from '../../../platform/lifecycle/open-external'
import { ControlPlaneError } from '../../../shared/contracts/protocol'
import type { CodexAccount, CodexAccountState, CodexLoginPhase, CodexLoginStatus, CodexQuotaReport, CodexQuotaResult, CodexQuotaWindow } from '../domain/codex'
import type { CodexDeviceLoginStart, CodexImportResult, CodexPort } from '../application/codex-port'

const LOGIN_PHASES: readonly CodexLoginPhase[] = ['idle', 'waiting', 'exchanging', 'success', 'error']
const ACCOUNT_STATES: readonly CodexAccountState[] = ['signed_out', 'signed_in', 'reauth_needed']

const signedOutAccount: CodexAccount = { state: 'signed_out', email: '', plan: '', accountId: '', providerId: '' }

type ImportPayload = { state?: string; email?: unknown; plan?: unknown; accountId?: unknown; providerId?: unknown; importedFrom?: unknown }
type QuotaPayload = {
  state?: string; email?: unknown; plan?: unknown; accountId?: unknown; providerId?: unknown
  quota?: unknown; error?: unknown
}
type QuotaReportPayload = { fetchedAt?: unknown; planType?: unknown; primary?: unknown; secondary?: unknown }
type QuotaWindowPayload = { present?: unknown; remainingPercent?: unknown; windowMinutes?: unknown; resetAt?: unknown }

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
    const result = this.#session.call<ImportPayload>('codex.import.json', { text }, signal)
    return result.then(coerceImportResult)
  }

  importFiles(paths: readonly string[], signal?: AbortSignal): Promise<CodexImportResult> {
    const result = this.#session.call<ImportPayload>('codex.import.files', { paths: [...paths] }, signal)
    return result.then(coerceImportResult)
  }

  status(signal?: AbortSignal): Promise<CodexAccount> {
    const result = this.#session.call<{ state?: string; email?: unknown; plan?: unknown; accountId?: unknown; providerId?: unknown }>('codex.status', undefined, signal)
    return result.then((value) => {
      const state = ACCOUNT_STATES.find((candidate) => candidate === value.state)
      if (!state) return signedOutAccount
      return {
        state,
        email: typeof value.email === 'string' ? value.email : '',
        plan: typeof value.plan === 'string' ? value.plan : '',
        accountId: typeof value.accountId === 'string' ? value.accountId : '',
        providerId: typeof value.providerId === 'string' ? value.providerId : '',
      }
    })
  }

  quota(signal?: AbortSignal): Promise<CodexQuotaResult> {
    const result = this.#session.call<QuotaPayload>('codex.quota', undefined, signal)
    return result.then((value) => {
      const state = ACCOUNT_STATES.find((candidate) => candidate === value.state)
      return {
        state: state ?? 'signed_out',
        email: typeof value.email === 'string' ? value.email : '',
        plan: typeof value.plan === 'string' ? value.plan : '',
        accountId: typeof value.accountId === 'string' ? value.accountId : '',
        providerId: typeof value.providerId === 'string' ? value.providerId : '',
        quota: coerceQuotaReport(value.quota),
        // A probe failure is a result field, never a transport rejection;
        // anything but text has nothing to say and stays silent.
        error: typeof value.error === 'string' && value.error.trim() !== '' ? value.error : undefined,
      }
    })
  }

  async logout(signal?: AbortSignal): Promise<void> {
    await this.#session.call('codex.logout', undefined, signal)
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

/** An import answer is the codex.status shape; a malformed one degrades to the
 * signed-out account, which the model reads as a failed import. */
function coerceImportResult(value: ImportPayload): CodexImportResult {
  const state = ACCOUNT_STATES.find((candidate) => candidate === value.state)
  return {
    state: state ?? 'signed_out',
    email: typeof value.email === 'string' ? value.email : '',
    plan: typeof value.plan === 'string' ? value.plan : '',
    accountId: typeof value.accountId === 'string' ? value.accountId : '',
    providerId: typeof value.providerId === 'string' ? value.providerId : '',
    importedFrom: typeof value.importedFrom === 'string' ? value.importedFrom : '',
  }
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
