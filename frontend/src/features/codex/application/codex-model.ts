import type { CodexAccount, CodexLoginStatus, CodexQuotaReport } from '../domain/codex'
import type { CodexImportResult, CodexPort } from './codex-port'
import { ControlPlaneError } from '../../../shared/contracts/protocol'

/** Client-side phase: the backend set plus 'connecting' (start sent, browser not open yet). */
export type CodexLoginUiPhase = 'idle' | 'connecting' | 'waiting' | 'exchanging' | 'success' | 'error'

/** The sign-in method the user picked in the pane; null until a flow starts. */
export type CodexAuthMethod = 'browser' | 'device' | 'importJson' | 'importFile'

export interface CodexModelState {
  readonly loginPhase: CodexLoginUiPhase
  readonly loginError: string
  readonly account: CodexAccount
  /** Held while a login is in flight; powers "Open the sign-in page again". */
  readonly authorizeUrl: string
  /** The method driving the current flow; tells the pane which copy to show. */
  readonly activeMethod: CodexAuthMethod | null
  /** Held while a device flow is live; the code the user must enter elsewhere. */
  readonly deviceUserCode: string
  /** Held while a device flow is live; the page the user opens themselves. */
  readonly deviceVerificationUrl: string
  /** Base file name after a file import; powers "Imported from <name>". */
  readonly importedFrom: string
  /** Readable text when the last disconnect attempt failed. */
  readonly logoutError: string
  /** The last settled usage probe; null until one has succeeded. */
  readonly quota: CodexQuotaReport | null
  /** True while a usage probe is in flight; disables the card's refresh. */
  readonly quotaPending: boolean
  /** Readable text when the last usage probe failed. */
  readonly quotaError: string
}

const initialAccount: CodexAccount = { state: 'signed_out', email: '', plan: '', accountId: '', providerId: '' }

const initialState: CodexModelState = {
  loginPhase: 'idle',
  loginError: '',
  account: initialAccount,
  authorizeUrl: '',
  activeMethod: null,
  deviceUserCode: '',
  deviceVerificationUrl: '',
  importedFrom: '',
  logoutError: '',
  quota: null,
  quotaPending: false,
  quotaError: '',
}

const TIMEOUT_COPY = 'The control plane did not answer in time. Check that the relay is running, then try again.'
const NOT_CONNECTED_COPY = 'The control plane is not connected. Wait for the relay, then try again.'
const GENERIC_LOGIN_COPY = 'Codex sign-in could not be started.'
const GENERIC_LOGOUT_COPY = 'Codex could not be disconnected.'
const PORT_IN_USE_COPY = 'The local sign-in port is already in use. Close the other sign-in attempt, then try again.'
const LOGIN_TIMED_OUT_COPY = 'The sign-in timed out before the browser answered. Try again.'
const LOGIN_CANCELLED_COPY = 'The sign-in was cancelled or denied before it completed.'
const SESSION_STORE_FAILED_COPY = 'Codex signed in, but the session could not be saved. Try again.'
const PROVISION_FAILED_COPY = 'Codex signed in, but the provider could not be added to the list. Try again.'
const EXCHANGE_FAILED_COPY = 'The sign-in could not be completed. Try again.'
const ACTIVE_PROVIDER_COPY = 'Codex is the active provider. Switch the active route away from Codex, then disconnect.'
const LOGIN_IN_PROGRESS_COPY = 'A sign-in is already in progress. Cancel it and try again.'
const GENERIC_IMPORT_COPY = 'The Codex credentials could not be imported.'
const REFRESH_TOKEN_REJECTED_COPY = 'The refresh token was rejected.'
const NO_USABLE_FILES_COPY = 'None of the selected files contained usable Codex credentials.'
const IMPORT_MULTIPLE_ACCOUNTS_COPY = 'The import contained multiple accounts; only one is supported.'
const NO_CREDENTIALS_FOUND_COPY = 'No Codex credentials were found in the input.'
const GENERIC_QUOTA_COPY = 'The Codex usage could not be loaded.'
const QUOTA_NOT_SIGNED_IN_COPY = 'Codex is no longer signed in. Sign in again from the Providers page.'
const QUOTA_SESSION_EXPIRED_COPY = 'The Codex session expired. Sign in again from the Providers page.'
const QUOTA_UNAUTHORIZED_COPY = 'The account rejected the usage request. Sign in again from the Providers page.'
const QUOTA_TIMED_OUT_COPY = 'The usage request timed out. Try again.'
/** The exact sentence the backend refuses a disconnect with while Codex is the active route. */
const ACTIVE_PROVIDER_ERROR = 'Codex is the active provider. Switch the active route away from Codex before disconnecting.'

/**
 * Translates the terse machine strings the backend reports for a login (or a
 * disconnect) into sentences the pane can render. The bare literals are
 * matched exactly; the provision and exchange failures carry a trailing
 * underlying error after their prefix, and the active-route refusal is
 * matched by containment because a failed logout can join several backend
 * errors into one newline-separated message. Anything unrecognised returns
 * null: the caller substitutes its own generic copy, so a raw backend or
 * provider string never reaches the alert region verbatim.
 */
function mapLoginErrorString(value: string): string | null {
  if (value === 'codex login timed out') return LOGIN_TIMED_OUT_COPY
  if (value === 'codex device login timed out') return LOGIN_TIMED_OUT_COPY
  if (value === 'codex login cancelled') return LOGIN_CANCELLED_COPY
  if (value === 'codex login is already in progress') return LOGIN_IN_PROGRESS_COPY
  if (value === 'codex session could not be stored') return SESSION_STORE_FAILED_COPY
  if (value.startsWith('codex provider could not be provisioned')) return PROVISION_FAILED_COPY
  if (value.startsWith('codex oauth exchange failed')) return EXCHANGE_FAILED_COPY
  if (value.includes(ACTIVE_PROVIDER_ERROR)) return ACTIVE_PROVIDER_COPY
  return null
}

/**
 * The import counterpart: the backend's import failures are matched by
 * containment, not exact equality, because they can carry a file name or a
 * count after the classified part. Unknown messages return null so the
 * caller's generic copy — never a raw backend string — is what the user sees.
 */
function mapImportErrorString(value: string): string | null {
  if (value.includes('refresh token was rejected')) return REFRESH_TOKEN_REJECTED_COPY
  if (value.includes('none of') && value.includes('files')) return NO_USABLE_FILES_COPY
  if (/found \d+ accounts/.test(value)) return IMPORT_MULTIPLE_ACCOUNTS_COPY
  if (value.includes('no codex credentials found')) return NO_CREDENTIALS_FOUND_COPY
  return null
}

/**
 * The quota counterpart: a failed probe is a result field the backend
 * reports as status text, and each family it can produce is matched before
 * the generic probe prefix — a deadline or an explicit rejection says more
 * than "the probe failed", and the not-signed-in refusals are a session
 * verdict, not a usage problem. Unknown messages return null so the
 * caller's generic copy — never a raw backend string — is shown.
 */
function mapQuotaErrorString(value: string): string | null {
  if (value === 'codex is not signed in') return QUOTA_NOT_SIGNED_IN_COPY
  if (value === 'codex session needs sign-in') return QUOTA_SESSION_EXPIRED_COPY
  if (value.includes('codex usage probe was unauthorized')) return QUOTA_UNAUTHORIZED_COPY
  if (value.includes('deadline exceeded')) return QUOTA_TIMED_OUT_COPY
  if (value.startsWith('codex oauth usage probe failed')) return GENERIC_QUOTA_COPY
  return null
}

/**
 * A port conflict is recognised only by its confirmed backend shape: the
 * login-failed code plus the listener adapter's raw bind error, which starts
 * with the port it could not bind and carries ' in use:'. Any other
 * codex_login_failed message (a dial failure, an already-running server)
 * is a different failure and falls back to the caller's generic copy.
 */
function isPortInUseLoginFailure(error: ControlPlaneError): boolean {
  return error.code === 'codex_login_failed'
    && error.message.startsWith('codex oauth port ')
    && error.message.includes(' in use:')
}

function readableError(error: unknown, generic: string, mapString: (value: string) => string | null = mapLoginErrorString): string {
  if (error instanceof ControlPlaneError) {
    if (error.code === 'timeout') return TIMEOUT_COPY
    if (error.code === 'not_connected' || error.code === 'disconnected') return NOT_CONNECTED_COPY
    if (isPortInUseLoginFailure(error)) return PORT_IN_USE_COPY
    // Raised by our own frontend adapter with an already-readable sentence;
    // platform copy, not provider text, so it is shown as written.
    if (error.code === 'insecure_url') return error.message
    return mapString(error.message) ?? generic
  }
  if (typeof error === 'string' && error.trim() !== '') return mapString(error) ?? generic
  if (error instanceof Error && error.message.trim() !== '') return mapString(error.message) ?? generic
  return generic
}

/**
 * Owns the Codex login and account state for the whole app. Backend pushes on
 * `codex.changed` only nudge: every event refetches `codex.status` and
 * `codex.login.status`, and the backend phase wins once a login is pending.
 */
export class CodexModel {
  readonly #port: CodexPort
  #state: CodexModelState = initialState
  readonly #listeners = new Set<() => void>()
  #unsubscribe: (() => void) | null = null
  #generation = 0
  #startAbort: AbortController | null = null
  /** Increments on every start; identifies which login attempt is live. */
  #loginEpoch = 0
  /** The epoch a cancel retired: this attempt's backend pushes are ignored until the next startLogin. */
  #cancelledEpoch: number | null = null

  constructor(port: CodexPort) {
    this.#port = port
  }

  readonly snapshot = (): CodexModelState => this.#state

  readonly subscribe = (listener: () => void): (() => void) => {
    this.#listeners.add(listener)
    return () => {
      this.#listeners.delete(listener)
    }
  }

  async connect(): Promise<void> {
    this.#unsubscribe ??= this.#port.subscribe(() => void this.refresh())
    await this.refresh()
  }

  /**
   * Refetches account and login status together. The backend phase is truth
   * except that a backend `idle` cannot contradict a client `connecting`
   * (the start call is still opening the browser), a `success` that the
   * still-signed-in account corroborates, or an unacknowledged `error`:
   * an unrelated push must not wipe an outcome the dialog has not shown yet.
   * The device fields live exactly as long as the device flow does: they are
   * kept while the flow is pending and dropped the moment it is not.
   */
  async refresh(): Promise<void> {
    const generation = ++this.#generation
    try {
      const [account, login] = await Promise.all([this.#port.status(), this.#port.loginStatus()])
      if (generation !== this.#generation) return
      // A cancelled login stays cancelled: the backend's cancel is advisory
      // and can lose the race with the token exchange, so the completion
      // pushes of a flow the user watched close are suppressed until they
      // start one again. Only the login is frozen — the account below still
      // merges, the backend remains the truth there.
      const phase = this.#cancelledEpoch === null ? this.#mergePhase(login, account) : 'idle'
      const loginError = phase === 'error'
        ? login.phase === 'error' ? mapLoginErrorString(login.error ?? '') ?? GENERIC_LOGIN_COPY : this.#state.loginError
        : ''
      this.#set({
        ...this.#state,
        account,
        loginPhase: phase,
        loginError,
        authorizeUrl: phase === 'connecting' || phase === 'waiting' || phase === 'exchanging'
          ? this.#state.authorizeUrl
          : '',
        deviceUserCode: phase === 'waiting' || phase === 'exchanging'
          ? login.deviceUserCode ?? this.#state.deviceUserCode
          : '',
        deviceVerificationUrl: phase === 'waiting' || phase === 'exchanging'
          ? login.deviceVerificationUrl ?? this.#state.deviceVerificationUrl
          : '',
      })
    } catch {
      // A failed ambient refetch keeps the last known state; the next
      // codex.changed nudge or reconnect refetch settles the truth.
    }
  }

  /** The dialog consumed a terminal outcome; the flow returns to idle. */
  acknowledgeOutcome(): void {
    const phase = this.#state.loginPhase
    if (phase !== 'success' && phase !== 'error') return
    this.#set({
      ...this.#state,
      loginPhase: 'idle',
      loginError: '',
      authorizeUrl: '',
      activeMethod: null,
      deviceUserCode: '',
      deviceVerificationUrl: '',
      importedFrom: '',
    })
  }

  async startLogin(): Promise<void> {
    const phase = this.#state.loginPhase
    if (phase !== 'idle' && phase !== 'error' && phase !== 'success') return
    // A fresh attempt retires whatever suppression a cancel armed: the pushes
    // of the flow this start joins belong to the UI again.
    this.#loginEpoch += 1
    this.#cancelledEpoch = null
    const controller = new AbortController()
    this.#startAbort = controller
    this.#set({ ...this.#settleFields('connecting'), loginPhase: 'connecting', activeMethod: 'browser' })
    try {
      const { authorizeUrl } = await this.#port.loginStart(controller.signal)
      if (controller.signal.aborted) return
      // The backend pushes `waiting` before login.start even returns, so the
      // phase can already have moved past `connecting` while this attempt
      // still has no URL. It must still get its URL and open the browser;
      // only a terminal phase means the flow died meanwhile.
      const settled = this.#state.loginPhase
      if (settled !== 'connecting' && settled !== 'waiting') return
      this.#set({ ...this.#state, authorizeUrl })
      await this.#port.openAuthorizeUrl(authorizeUrl)
      if (controller.signal.aborted) return
      if (this.#state.loginPhase === 'connecting') {
        this.#set({ ...this.#state, loginPhase: 'waiting' })
      }
    } catch (error) {
      if (controller.signal.aborted) return // cancelLogin already reset the flow
      this.#set({ ...this.#settleFields('error'), loginPhase: 'error', loginError: readableError(error, GENERIC_LOGIN_COPY) })
    }
  }

  /**
   * Starts the device-code login. The verification page is never opened by
   * this call or by the model: the user reads the code, opens the page in a
   * browser they choose, and enters it there.
   */
  async startDeviceLogin(): Promise<void> {
    const phase = this.#state.loginPhase
    if (phase !== 'idle' && phase !== 'error' && phase !== 'success') return
    // A fresh attempt retires whatever suppression a cancel armed: the pushes
    // of the flow this start joins belong to the UI again.
    this.#loginEpoch += 1
    this.#cancelledEpoch = null
    const controller = new AbortController()
    this.#startAbort = controller
    this.#set({ ...this.#settleFields('connecting'), loginPhase: 'connecting', activeMethod: 'device' })
    try {
      const { userCode, verificationUrl } = await this.#port.deviceLoginStart(controller.signal)
      if (controller.signal.aborted) return
      // Same leniency as startLogin: the backend can push `waiting` before
      // the start call returns; only a terminal phase means the flow died.
      const settled = this.#state.loginPhase
      if (settled !== 'connecting' && settled !== 'waiting') return
      this.#set(
        settled === 'connecting'
          ? { ...this.#state, loginPhase: 'waiting', deviceUserCode: userCode, deviceVerificationUrl: verificationUrl }
          : { ...this.#state, deviceUserCode: userCode, deviceVerificationUrl: verificationUrl },
      )
    } catch (error) {
      if (controller.signal.aborted) return // cancelLogin already reset the flow
      this.#set({ ...this.#settleFields('error'), loginPhase: 'error', loginError: readableError(error, GENERIC_LOGIN_COPY) })
    }
  }

  /** Imports pasted credential text; the text never enters model state. */
  importFromJson(text: string): Promise<void> {
    return this.#startImport('importJson', (signal) => this.#port.importJson(text, signal))
  }

  /** Imports credentials from files the platform picker returned. */
  importFromFiles(paths: readonly string[]): Promise<void> {
    return this.#startImport('importFile', (signal) => this.#port.importFiles(paths, signal))
  }

  /**
   * Shared spine of both imports: one attempt, one epoch, one abort
   * controller, terminal handling, and readable errors. The credential text
   * itself stays inside the port call — only the resulting account and, for
   * files, the base file name reach the state.
   */
  async #startImport(method: CodexAuthMethod, run: (signal: AbortSignal) => Promise<CodexImportResult>): Promise<void> {
    const phase = this.#state.loginPhase
    if (phase !== 'idle' && phase !== 'error' && phase !== 'success') return
    this.#loginEpoch += 1
    this.#cancelledEpoch = null
    const controller = new AbortController()
    this.#startAbort = controller
    this.#set({ ...this.#settleFields('connecting'), loginPhase: 'connecting', activeMethod: method })
    try {
      const result = await run(controller.signal)
      if (controller.signal.aborted) return
      // Terminal or waiting means another flow took over while this import
      // was in flight; its outcome owns the pane, not this late result.
      if (this.#state.loginPhase !== 'connecting') return
      if (result.state !== 'signed_in') {
        this.#set({ ...this.#state, loginPhase: 'error', loginError: GENERIC_IMPORT_COPY })
        return
      }
      this.#set({
        ...this.#state,
        loginPhase: 'success',
        account: {
          state: result.state,
          email: result.email,
          plan: result.plan,
          accountId: result.accountId,
          providerId: result.providerId,
        },
        importedFrom: method === 'importFile' ? result.importedFrom ?? '' : '',
      })
    } catch (error) {
      if (controller.signal.aborted) return // cancelLogin already reset the flow
      this.#set({ ...this.#settleFields('error'), loginPhase: 'error', loginError: readableError(error, GENERIC_IMPORT_COPY, mapImportErrorString) })
    }
  }

  /** Safe in any phase; a backend that has nothing pending no-ops. */
  async cancelLogin(): Promise<void> {
    this.#startAbort?.abort()
    this.#startAbort = null
    // The backend's cancel is advisory and can lose the race with the token
    // exchange, so suppression is armed synchronously here, before the
    // request: whenever that exchange completes and pushes, the login the
    // user watched close stays closed until they start one again.
    this.#cancelledEpoch = this.#loginEpoch
    this.#set({
      ...this.#settleFields('idle'),
      loginPhase: 'idle',
      activeMethod: null,
    })
    try {
      await this.#port.loginCancel()
    } catch {
      // A dropped cancel must not wedge the dialog: the account refetches
      // still tell the truth, and the backend ignores a cancel with nothing
      // pending.
    }
  }

  /** Re-opens the held authorize URL; the login itself is unaffected if the open fails. */
  async reopenAuthorizeUrl(): Promise<void> {
    if (this.#state.authorizeUrl === '') return
    try {
      await this.#port.openAuthorizeUrl(this.#state.authorizeUrl)
    } catch {
      // Staying in 'waiting' is correct: the pending login does not depend on
      // this click, and the user can simply click again.
    }
  }

  /** Opens the device verification page on an explicit click; the flow is unaffected if the open fails. */
  async openVerificationUrl(): Promise<void> {
    if (this.#state.deviceVerificationUrl === '') return
    try {
      await this.#port.openAuthorizeUrl(this.#state.deviceVerificationUrl)
    } catch {
      // Staying in 'waiting' is correct: the pending device login does not
      // depend on this click, and the user can simply click again.
    }
  }

  async logout(): Promise<boolean> {
    this.#set({ ...this.#state, logoutError: '' })
    try {
      await this.#port.logout()
      await this.refresh()
      return true
    } catch (error) {
      this.#set({ ...this.#state, logoutError: readableError(error, GENERIC_LOGOUT_COPY) })
      return false
    }
  }

  /**
   * Probes the account's usage windows on demand — the quota card's own
   * fetch, deliberately outside the ambient nudge refetch: pushes refetch
   * account and login status, while usage is read when the card mounts,
   * when its button is clicked and when the account state changes.
   * A failed probe is a result field: the answer carries the last good
   * windows alongside the error, so a failed refresh is a stale card with
   * a reason, never a wiped one. Re-entrant calls are refused while one is
   * in flight; the backend coalesces concurrent probes besides.
   */
  async refreshQuota(): Promise<void> {
    if (this.#state.quotaPending) return
    this.#set({ ...this.#state, quotaPending: true })
    try {
      const result = await this.#port.quota()
      this.#set({
        ...this.#state,
        quotaPending: false,
        quota: result.quota ?? this.#state.quota,
        quotaError: result.error !== undefined
          ? mapQuotaErrorString(result.error) ?? GENERIC_QUOTA_COPY
          : '',
      })
    } catch (error) {
      this.#set({ ...this.#state, quotaPending: false, quotaError: readableError(error, GENERIC_QUOTA_COPY, mapQuotaErrorString) })
    }
  }

  dispose(): void {
    this.#unsubscribe?.()
    this.#unsubscribe = null
    this.#listeners.clear()
  }

  /**
   * The per-flow fields a terminal or freshly started attempt must not
   * inherit from the previous one: error text, held URLs, device fields and
   * the import source name. The next start fills in its own.
   */
  #settleFields(phase: CodexLoginUiPhase): CodexModelState {
    return {
      ...this.#state,
      loginPhase: phase,
      loginError: '',
      authorizeUrl: '',
      deviceUserCode: '',
      deviceVerificationUrl: '',
      importedFrom: '',
    }
  }

  #mergePhase(login: CodexLoginStatus, account: CodexAccount): CodexLoginUiPhase {
    if (login.phase !== 'idle') return login.phase
    const current = this.#state.loginPhase
    if (current === 'connecting') return 'connecting'
    // Sticky success only while the account really is signed in: a nudge
    // that reports reauth_needed (the session expired right after linking)
    // must not keep announcing a success that is already gone.
    if (current === 'success' && account.state === 'signed_in') return 'success'
    if (current === 'error') return 'error'
    return 'idle'
  }

  #set(state: CodexModelState): void {
    this.#state = state
    for (const listener of this.#listeners) listener()
  }
}
