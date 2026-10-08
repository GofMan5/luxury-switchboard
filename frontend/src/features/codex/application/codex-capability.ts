export function hasCodexLogin(capabilities: readonly string[]): boolean {
  return capabilities.includes('codex.login')
}

/** `codex.quota` answers the account's usage windows; a shell without it
 * keeps the key list, so the card is gated on this, not on the login flow. */
export function hasCodexQuota(capabilities: readonly string[]): boolean {
  return capabilities.includes('codex.quota')
}
