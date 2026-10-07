import type { CodexAccount, CodexLoginStatus } from '../domain/codex'

/** Result of `codex.login.device.start`: what the user must enter, and where. */
export interface CodexDeviceLoginStart {
  readonly userCode: string
  readonly verificationUrl: string
  readonly pollIntervalSeconds: number
}

/** Result of the import commands: the codex.status shape, files also name their source. */
export type CodexImportResult = CodexAccount & { readonly importedFrom?: string }

export interface CodexPort {
  loginStart(signal?: AbortSignal): Promise<{ authorizeUrl: string }>
  loginStatus(signal?: AbortSignal): Promise<CodexLoginStatus>
  loginCancel(signal?: AbortSignal): Promise<void>
  /** Starts the device-code login; nothing is opened in a browser by this call. */
  deviceLoginStart(signal?: AbortSignal): Promise<CodexDeviceLoginStart>
  importJson(text: string, signal?: AbortSignal): Promise<CodexImportResult>
  importFiles(paths: readonly string[], signal?: AbortSignal): Promise<CodexImportResult>
  status(signal?: AbortSignal): Promise<CodexAccount>
  logout(signal?: AbortSignal): Promise<void>
  /** Opens the authorize URL in the default browser after the https check. */
  openAuthorizeUrl(url: string): Promise<void>
  /** codex.changed push; a nudge only — the model refetches on every event. */
  subscribe(listener: () => void): () => void
}
