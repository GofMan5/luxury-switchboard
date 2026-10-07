// Mirrors the backend contract; names are the wire names, not renamed.
export type CodexLoginPhase = 'idle' | 'waiting' | 'exchanging' | 'success' | 'error'
export type CodexAccountState = 'signed_out' | 'signed_in' | 'reauth_needed'

export interface CodexLoginStatus {
  readonly phase: CodexLoginPhase
  readonly error?: string
  /** Present only while a device login is live; the code the user must enter. */
  readonly deviceUserCode?: string
  /** Present only while a device login is live; where the user enters it. */
  readonly deviceVerificationUrl?: string
}

export interface CodexAccount {
  readonly state: CodexAccountState
  readonly email: string // '' when signed_out
  readonly plan: string // backend text, '' when unknown — the UI never invents it
  readonly accountId: string
  readonly providerId: string // the managed provider's id, '' when none
}
