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

/** One usage window the account's endpoint reported. present=false means the
 * endpoint said nothing about this window: there is no meter to draw, and
 * remainingPercent carries the neutral full value so nothing renders broken. */
export interface CodexQuotaWindow {
  readonly present: boolean
  /** How much of the window is LEFT, 0–100 — the meter fills on remaining. */
  readonly remainingPercent: number
  /** Length of the window in minutes; undefined when the endpoint did not say. */
  readonly windowMinutes?: number
  /** When the window empties again, Unix seconds; undefined when unknown. */
  readonly resetAt?: number
}

/** One settled usage probe: when it landed and both windows. planType is the
 * upstream plan name — a fallback label only, never invented client-side. */
export interface CodexQuotaReport {
  readonly fetchedAt: number // Unix seconds; 0 never reaches this type
  readonly planType?: string
  readonly primary: CodexQuotaWindow
  readonly secondary: CodexQuotaWindow
}

/** Result of `codex.quota`: the account the answer was taken for, plus its
 * last settled usage probe. The card labels (email, plan, state) live in the
 * status rows — a quota answer carries usage only, never identity. quota is
 * absent until some probe has succeeded; error carries the last probe's
 * failure as status text only. */
export interface CodexQuotaResult {
  readonly accountId: string
  readonly quota?: CodexQuotaReport
  readonly error?: string
}

/** Result of `codex.status`: every account row the backend reports plus the
 * aggregate state over those rows. accounts is always a list — even signed
 * out, the answer is "no rows", never a missing field. */
export interface CodexStatus {
  readonly state: CodexAccountState
  readonly accounts: readonly CodexAccount[]
}
