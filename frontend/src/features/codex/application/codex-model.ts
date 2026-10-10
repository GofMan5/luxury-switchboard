import type { CodexAccount, CodexAccountState, CodexLoginStatus, CodexQuotaReport, CodexStatus } from '../domain/codex'
import type { CodexImportResult, CodexPort } from './codex-port'
import { ControlPlaneError } from '../../../shared/contracts/protocol'

/** Client-side phase: the backend set plus 'connecting' (start sent, no answer yet). */
export type CodexLoginUiPhase = 'idle' | 'connecting' | 'waiting' | 'exchanging' | 'success' | 'error'

/** The sign-in method the user picked; null until a flow starts. */
export type CodexAuthMethod = 'browser' | 'device' | 'importJson' | 'importFile'

/** One account's usage card: the last good windows, why the last probe failed, and whether one runs. */
export interface CodexQuotaCard {
  readonly quota: CodexQuotaReport | null
  readonly error: string
  readonly pending: boolean
}

/** Everything the Codex panes render; one object per publish, so React sees a stable snapshot. */
export interface CodexModelState {
  readonly loginPhase: CodexLoginUiPhase
  /** The method driving the current or last flow; tells the pane which copy to show. */
  readonly activeMethod: CodexAuthMethod | null
  /** The aggregate the backend computed over every account. */
  readonly state: CodexAccountState
  readonly accounts: readonly CodexAccount[]
  /** The account the last completed flow added or brought back — the one the success card addresses. */
  readonly freshAccount: CodexAccount | null
  readonly quotas: Readonly<Record<string, CodexQuotaCard>>
  readonly loginError: string
  /** Held while a browser login is in flight; powers "Open the sign-in page again". */
  readonly authorizeUrl: string
  /** Held while a device flow is live; the code the user must enter elsewhere. */
  readonly deviceUserCode: string
  /** Held while a device flow is live; the page the user opens themselves. */
  readonly deviceVerificationUrl: string
  /** Base file name after a file import; powers "Imported from <name>". */
  readonly importedFrom: string
  /** Readable text when the last disconnect attempt failed. */
  readonly logoutError: string
}

const initialState: CodexModelState = {
  loginPhase: 'idle',
  activeMethod: null,
  state: 'signed_out',
  accounts: [],
  freshAccount: null,
  quotas: {},
  loginError: '',
  authorizeUrl: '',
  deviceUserCode: '',
  deviceVerificationUrl: '',
  importedFrom: '',
  logoutError: '',
}

const TIMEOUT_COPY = 'The control plane did not answer in time. Check that the relay is running, then try again.'
const NOT_CONNECTED_COPY = 'The control plane is not connected. Wait for the relay, then try again.'
const GENERIC_LOGIN_COPY = 'Codex sign-in could not be started.'
const GENERIC_PHASE_COPY = 'Codex sign-in could not be completed.'
const GENERIC_LOGOUT_COPY = 'Codex could not be disconnected.'
/** A removal is a heavier verb than a disconnect: its generic copy names the fallback, not just the failure. */
const GENERIC_REMOVE_COPY = 'Codex could not be removed. Try again, or disconnect it instead.'
const PORT_IN_USE_COPY = 'The local sign-in port is already in use. Close the other sign-in attempt, then try again.'
const LOGIN_TIMED_OUT_COPY = 'The sign-in timed out before the browser answered. Try again.'
const DEVICE_TIMED_OUT_COPY = 'The device sign-in timed out before it completed. Try again.'
const LOGIN_CANCELLED_COPY = 'The sign-in was cancelled or denied before it completed.'
const SESSION_STORE_FAILED_COPY = 'Codex signed in, but the session could not be saved. Try again.'
const PROVISION_FAILED_COPY = 'Codex signed in, but the provider could not be added to the list. Try again.'
const EXCHANGE_FAILED_COPY = 'The sign-in could not be completed. Try again.'
const GENERIC_IMPORT_COPY = 'The Codex credentials could not be imported.'
const NO_USABLE_FILES_COPY = 'None of the selected files held Codex credentials. Pick the file that was exported from Codex.'
/** The stale-pair outcome the backend now names: the refresh exchange ran and the server refused it. */
const REFRESH_REJECTED_COPY = 'The Codex sign-in server refused these credentials. Export fresh credentials from Codex, then import them again.'
/** The refresh exchange could not run at all; the credentials themselves were never judged. */
const IMPORT_UNREACHABLE_COPY = 'The Codex sign-in server could not be reached. Check the connection, then try again.'
const GENERIC_QUOTA_COPY = 'The Codex usage could not be loaded.'
const QUOTA_NOT_SIGNED_IN_COPY = 'The account is no longer signed in. Sign in again to see its usage.'
/** The refusal text the backend emits when Codex backs the active route; matched by containment, joined errors included. */
const ACTIVE_PROVIDER_MARKER = 'Codex is the active provider. Switch the active route away from Codex before disconnecting.'
const ACTIVE_PROVIDER_COPY = 'Codex is the active provider. Switch the active route away from Codex, then disconnect.'

/** Maps errors thrown by a start call (browser or device) to copy; null means "no special case". */
function mapLoginErrorString(value: string): string | null {
  // The backend reports a busy local sign-in port through the message, under several codes.
  if (value.includes('address already in use')) return PORT_IN_USE_COPY
  return null
}

/** Maps import failures the user can act on; the markers are the backend's own refusal wording, joined errors included. */
function mapImportError(value: string): string | null {
  if (value.includes('none of the selected files held codex credentials')) return NO_USABLE_FILES_COPY
  // The rejection wrapper carries the unreachable text inside it, so it must be matched first.
  if (value.includes('codex refresh token was rejected')) return REFRESH_REJECTED_COPY
  if (value.includes('codex oauth refresh failed')) return IMPORT_UNREACHABLE_COPY
  return null
}

/** Maps per-account quota refusals to card copy. */
function mapQuotaError(value: string): string {
  if (value === 'codex is not signed in') return QUOTA_NOT_SIGNED_IN_COPY
  return GENERIC_QUOTA_COPY
}

/** Maps backend login-phase error strings pushed on `codex.changed`; never passes raw text through. */
function mapLoginPhaseError(value: string): string {
  if (value === 'codex login timed out') return LOGIN_TIMED_OUT_COPY
  if (value === 'codex device login timed out') return DEVICE_TIMED_OUT_COPY
  if (value === 'codex login cancelled') return LOGIN_CANCELLED_COPY
  if (value === 'codex session could not be stored') return SESSION_STORE_FAILED_COPY
  if (value.startsWith('codex provider could not be provisioned')) return PROVISION_FAILED_COPY
  if (value.startsWith('codex oauth exchange failed')) return EXCHANGE_FAILED_COPY
  return GENERIC_PHASE_COPY
}

/** Translates a thrown error to alert-region copy; raw backend text never survives this. */
function readableError(error: unknown, generic: string, mapString: (value: string) => string | null = mapLoginErrorString): string {
  if (error instanceof ControlPlaneError) {
    if (error.code === 'timeout') return TIMEOUT_COPY
    if (error.code === 'not_connected' || error.code === 'disconnected') return NOT_CONNECTED_COPY
    // Platform-generated copy (the https check lives in the port), so it is already user-facing.
    if (error.code === 'insecure_url') return error.message
    return mapString(error.message) ?? generic
  }
  if (typeof error === 'string') return mapString(error) ?? generic
  if (error instanceof Error && error.message !== '') return mapString(error.message) ?? generic
  return generic
}

/** Translates a thrown quota-probe error to card copy. */
function readableQuotaError(error: unknown): string {
  if (error instanceof ControlPlaneError) {
    if (error.code === 'timeout') return TIMEOUT_COPY
    if (error.code === 'not_connected' || error.code === 'disconnected') return NOT_CONNECTED_COPY
    return mapQuotaError(error.message)
  }
  if (typeof error === 'string') return mapQuotaError(error)
  if (error instanceof Error && error.message !== '') return mapQuotaError(error.message)
  return GENERIC_QUOTA_COPY
}

/** Translates a thrown logout error; the active-route refusal outranks every generic copy. */
function mapLogoutFailure(error: unknown, remove: boolean): string {
  if (error instanceof ControlPlaneError) {
    if (error.code === 'timeout') return TIMEOUT_COPY
    if (error.code === 'not_connected' || error.code === 'disconnected') return NOT_CONNECTED_COPY
    if (error.message.includes(ACTIVE_PROVIDER_MARKER)) return ACTIVE_PROVIDER_COPY
  }
  if (error instanceof Error && error.message.includes(ACTIVE_PROVIDER_MARKER)) return ACTIVE_PROVIDER_COPY
  return remove ? GENERIC_REMOVE_COPY : GENERIC_LOGOUT_COPY
}

export class CodexModel {
  readonly #port: CodexPort
  #state: CodexModelState = initialState
  /** Bumped by every flow start and cancel; a late answer from an older flow is dropped by number. */
  #flowSeq = 0
  #flowAbort: AbortController | null = null
  /** Set by cancelLogin, cleared by the next #beginFlow; a push must not resurrect a cancelled flow's phase. */
  #flowDead = false
  /** Bumped by every refetch; a late refetch must not overwrite a newer one. */
  #generation = 0
  /** False until the first status answer; that answer applies accounts from scratch. */
  #loaded = false
  #unsubscribe: (() => void) | null = null
  readonly #listeners = new Set<() => void>()

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
    this.#unsubscribe ??= this.#port.subscribe(() => void this.#refetch('merge'))
    await this.#refetch('adopt')
  }

  async refresh(): Promise<void> {
    await this.#refetch('adopt')
  }

  async startLogin(): Promise<void> {
    if (this.#flowLive()) return
    const flow = this.#beginFlow('browser')
    try {
      const { authorizeUrl } = await this.#port.loginStart(flow.signal)
      if (!this.#flowCurrent(flow.seq, flow.signal)) return
      // A push may have settled the flow while the call ran; never open a browser for a dead login.
      if (this.#state.loginPhase !== 'connecting' && this.#state.loginPhase !== 'waiting') return
      this.#set({ ...this.#state, authorizeUrl })
      await this.#port.openAuthorizeUrl(authorizeUrl)
      if (!this.#flowCurrent(flow.seq, flow.signal)) return
      if (this.#state.loginPhase === 'connecting') this.#set({ ...this.#state, loginPhase: 'waiting' })
    } catch (error) {
      if (!this.#flowCurrent(flow.seq, flow.signal)) return
      this.#set({ ...this.#settleFields('error'), loginError: readableError(error, GENERIC_LOGIN_COPY) })
    }
  }

  async startDeviceLogin(): Promise<void> {
    if (this.#flowLive()) return
    const flow = this.#beginFlow('device')
    try {
      const start = await this.#port.deviceLoginStart(flow.signal)
      if (!this.#flowCurrent(flow.seq, flow.signal)) return
      if (this.#state.loginPhase !== 'connecting' && this.#state.loginPhase !== 'waiting') return
      // The user opens the verification page themselves; this call opens nothing.
      this.#set({
        ...this.#state,
        loginPhase: 'waiting',
        deviceUserCode: start.userCode,
        deviceVerificationUrl: start.verificationUrl,
      })
    } catch (error) {
      if (!this.#flowCurrent(flow.seq, flow.signal)) return
      this.#set({ ...this.#settleFields('error'), loginError: readableError(error, GENERIC_LOGIN_COPY) })
    }
  }

  async cancelLogin(): Promise<void> {
    this.#flowSeq += 1
    this.#flowDead = true
    this.#flowAbort?.abort()
    this.#flowAbort = null
    this.#set({ ...this.#settleFields('idle'), activeMethod: null })
    try {
      await this.#port.loginCancel()
    } catch {
      // The backend flow ends on its own; a failed nudge must not block the reset.
    }
  }

  async importFromJson(text: string): Promise<void> {
    await this.#runImport('importJson', (signal) => this.#port.importJson(text, signal), false)
  }

  async importFromFiles(paths: readonly string[]): Promise<void> {
    await this.#runImport('importFile', (signal) => this.#port.importFiles(paths, signal), true)
  }

  async refreshQuota(accountId: string): Promise<void> {
    if (!this.#state.accounts.some((account) => account.accountId === accountId)) return
    const previous = this.#state.quotas[accountId]
    if (previous?.pending) return
    // A failing re-probe keeps the last good windows: usage data ages, it does not vanish.
    const lastGood = previous?.quota ?? null
    this.#setQuota(accountId, { quota: lastGood, error: '', pending: true })
    try {
      const result = await this.#port.quota(accountId)
      if (!this.#state.accounts.some((account) => account.accountId === accountId)) return
      if (result.quota !== undefined) {
        this.#setQuota(accountId, { quota: result.quota, error: '', pending: false })
      } else {
        this.#setQuota(accountId, { quota: lastGood, error: mapQuotaError(result.error ?? ''), pending: false })
      }
    } catch (error) {
      if (!this.#state.accounts.some((account) => account.accountId === accountId)) return
      this.#setQuota(accountId, { quota: lastGood, error: readableQuotaError(error), pending: false })
    }
  }

  async logout(accountId: string | null, remove = false): Promise<boolean> {
    this.#set({ ...this.#state, logoutError: '' })
    try {
      await this.#port.logout(accountId, remove)
      await this.#refetch('adopt')
      return true
    } catch (error) {
      this.#set({ ...this.#state, logoutError: mapLogoutFailure(error, remove) })
      return false
    }
  }

  acknowledgeOutcome(): void {
    if (this.#state.loginPhase !== 'success' && this.#state.loginPhase !== 'error') return
    this.#set({ ...this.#settleFields('idle'), activeMethod: null, freshAccount: null })
  }

  async reopenAuthorizeUrl(): Promise<void> {
    if (this.#state.authorizeUrl === '') return
    try {
      await this.#port.openAuthorizeUrl(this.#state.authorizeUrl)
    } catch {
      // Opening the page is a convenience; a failure must not disturb the running flow.
    }
  }

  async openVerificationUrl(): Promise<void> {
    if (this.#state.deviceVerificationUrl === '') return
    try {
      await this.#port.openAuthorizeUrl(this.#state.deviceVerificationUrl)
    } catch {
      // Same: the user can open the page by hand.
    }
  }

  dispose(): void {
    this.#unsubscribe?.()
    this.#unsubscribe = null
    this.#listeners.clear()
  }

  #flowLive(): boolean {
    const phase = this.#state.loginPhase
    return phase === 'connecting' || phase === 'waiting' || phase === 'exchanging'
  }

  #beginFlow(method: CodexAuthMethod): { seq: number; signal: AbortSignal } {
    this.#flowSeq += 1
    this.#flowDead = false
    const controller = new AbortController()
    this.#flowAbort = controller
    // Accounts, usage rows and the fresh account survive a flow start; only flow fields reset.
    this.#set({ ...this.#settleFields('connecting'), activeMethod: method })
    return { seq: this.#flowSeq, signal: controller.signal }
  }

  #flowCurrent(seq: number, signal: AbortSignal): boolean {
    return seq === this.#flowSeq && !signal.aborted
  }

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

  async #runImport(
    method: 'importJson' | 'importFile',
    call: (signal: AbortSignal) => Promise<CodexImportResult>,
    keepImportedFrom: boolean,
  ): Promise<void> {
    if (this.#flowLive()) return
    const flow = this.#beginFlow(method)
    try {
      const result = await call(flow.signal)
      // A cancel while the import ran discards the result whole: accounts must
      // not appear behind the user's back after they stopped the flow.
      if (!this.#flowCurrent(flow.seq, flow.signal)) return
      if (result.state === 'signed_in') {
        this.#set({
          ...this.#settleFields('success'),
          ...this.#applyAccounts(result, false),
          importedFrom: keepImportedFrom ? result.importedFrom ?? '' : '',
        })
      } else {
        this.#set({ ...this.#settleFields('error'), loginError: GENERIC_IMPORT_COPY })
      }
    } catch (error) {
      if (!this.#flowCurrent(flow.seq, flow.signal)) return
      this.#set({ ...this.#settleFields('error'), loginError: readableError(error, GENERIC_IMPORT_COPY, mapImportError) })
    }
  }

  async #refetch(mode: 'adopt' | 'merge'): Promise<void> {
    const generation = ++this.#generation
    try {
      const [status, login] = await Promise.all([this.#port.status(), this.#port.loginStatus()])
      if (generation !== this.#generation) return
      const phase =
        mode === 'adopt' ? login.phase : this.#flowDead ? this.#state.loginPhase : this.#mergePhase(login, status.state)
      const loginError =
        phase === 'error' ? (login.phase === 'error' ? mapLoginPhaseError(login.error ?? '') : this.#state.loginError) : ''
      const flowLivePhase = phase === 'connecting' || phase === 'waiting' || phase === 'exchanging'
      this.#set({
        ...this.#state,
        ...this.#applyAccounts(status, !this.#loaded),
        loginPhase: phase,
        loginError,
        authorizeUrl: flowLivePhase ? this.#state.authorizeUrl : '',
        deviceUserCode:
          phase === 'waiting' || phase === 'exchanging' ? login.deviceUserCode ?? this.#state.deviceUserCode : '',
        deviceVerificationUrl:
          phase === 'waiting' || phase === 'exchanging' ? login.deviceVerificationUrl ?? this.#state.deviceVerificationUrl : '',
      })
      this.#loaded = true
    } catch {
      // A failed nudge keeps the last known state; the next push or refresh retries.
    }
  }

  #applyAccounts(
    status: Pick<CodexStatus, 'state' | 'accounts'>,
    initial: boolean,
  ): Pick<CodexModelState, 'state' | 'accounts' | 'freshAccount' | 'quotas'> {
    if (initial) {
      return { state: status.state, accounts: status.accounts, freshAccount: null, quotas: {} }
    }
    const previousById = new Map(this.#state.accounts.map((account) => [account.accountId, account]))
    let fresh: CodexAccount | null = null
    for (const account of status.accounts) {
      const before = previousById.get(account.accountId)
      const appeared = before === undefined
      const recovered = before !== undefined && before.state === 'reauth_needed' && account.state === 'signed_in'
      // With several new rows at once (a multi-account import), the last one is the one the success card addresses.
      if (appeared || recovered) fresh = account
    }
    if (fresh === null && this.#state.freshAccount !== null) {
      const current = this.#state.freshAccount
      if (status.accounts.some((account) => account.accountId === current.accountId)) fresh = current
    }
    const quotas: Record<string, CodexQuotaCard> = {}
    for (const account of status.accounts) {
      const card = this.#state.quotas[account.accountId]
      if (card !== undefined) quotas[account.accountId] = card
    }
    return { state: status.state, accounts: status.accounts, freshAccount: fresh, quotas }
  }

  #mergePhase(login: CodexLoginStatus, nextState: CodexAccountState): CodexLoginUiPhase {
    if (login.phase !== 'idle') return login.phase
    const current = this.#state.loginPhase
    if (current === 'connecting') return 'connecting'
    // Sticky success only while the aggregate really is signed in: a nudge that
    // reports reauth_needed (the session expired right after linking) must not
    // keep announcing a success that is already gone.
    if (current === 'success' && nextState === 'signed_in') return 'success'
    if (current === 'error') return 'error'
    return 'idle'
  }

  #setQuota(accountId: string, card: CodexQuotaCard): void {
    this.#set({ ...this.#state, quotas: { ...this.#state.quotas, [accountId]: card } })
  }

  #set(state: CodexModelState): void {
    this.#state = state
    for (const listener of this.#listeners) listener()
  }
}
