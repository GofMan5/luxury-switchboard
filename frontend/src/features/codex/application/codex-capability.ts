export function hasCodexLogin(capabilities: readonly string[]): boolean {
  return capabilities.includes('codex.login')
}

/** `codex.quota` answers the account's usage windows; a shell without it
 * keeps the key list, so the card is gated on this, not on the login flow. */
export function hasCodexQuota(capabilities: readonly string[]): boolean {
  return capabilities.includes('codex.quota')
}

/** `codex.quota.all` probes every account in one call — the bulk button. */
export function hasCodexQuotaAll(capabilities: readonly string[]): boolean {
  return capabilities.includes('codex.quota.all')
}

/** `codex.wake` re-refreshes tokens for sleeping sessions; without it the
 * bulk Wake control stays hidden instead of promising an unsupported call. */
export function hasCodexWake(capabilities: readonly string[]): boolean {
  return capabilities.includes('codex.wake')
}
