import type { CodexAccountState, CodexAccount, CodexLoginStatus, CodexQuotaResult, CodexStatus, CodexWakeResult } from '../domain/codex'

/** Result of `codex.quota.all`: every account's probe in one answer, the
 * same per-account rows `codex.quota` answers with. */
export interface CodexQuotaAllResult {
  readonly results: readonly CodexQuotaResult[]
}

/** Result of `codex.login.device.start`: what the user must enter, and where. */
export interface CodexDeviceLoginStart {
  readonly userCode: string
  readonly verificationUrl: string
  readonly pollIntervalSeconds: number
}

/** Result of the import commands: the codex.status shape, files also name their source. */
export interface CodexImportResult {
  readonly state: CodexAccountState
  readonly accounts: readonly CodexAccount[]
  readonly importedFrom?: string
}

export interface CodexPort {
  loginStart(signal?: AbortSignal): Promise<{ authorizeUrl: string }>
  loginStatus(signal?: AbortSignal): Promise<CodexLoginStatus>
  loginCancel(signal?: AbortSignal): Promise<void>
  /** Starts the device-code login; nothing is opened in a browser by this call. */
  deviceLoginStart(signal?: AbortSignal): Promise<CodexDeviceLoginStart>
  importJson(text: string, signal?: AbortSignal): Promise<CodexImportResult>
  importFiles(paths: readonly string[], signal?: AbortSignal): Promise<CodexImportResult>
  status(signal?: AbortSignal): Promise<CodexStatus>
  /** Probes one account's usage windows; a failed probe is a result field,
   * not a rejection — the last good windows ride the same answer. */
  quota(accountId: string, signal?: AbortSignal): Promise<CodexQuotaResult>
  /** Probes every account's usage windows in one call; per-account failures
   * stay result fields, exactly like the single probe. */
  quotaAll(signal?: AbortSignal): Promise<CodexQuotaAllResult>
  /** Asks the backend to refresh tokens for the named accounts (or every
   * account when accountIds is null). A wake that could not run answers with
   * per-account error rows, not a rejection. */
  wake(accountIds: readonly string[] | null, signal?: AbortSignal): Promise<CodexWakeResult>
  /** A disconnect clears a session; accountId null means every account.
   * `remove: true` also deletes the provisioned provider entries and their
   * routes via the backend cascade. */
  logout(accountId: string | null, remove: boolean, signal?: AbortSignal): Promise<void>
  /** Opens the authorize URL in the default browser after the https check. */
  openAuthorizeUrl(url: string): Promise<void>
  /** codex.changed push; a nudge only — the model refetches on every event. */
  subscribe(listener: () => void): () => void
}
