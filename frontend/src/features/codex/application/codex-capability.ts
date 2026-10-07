export function hasCodexLogin(capabilities: readonly string[]): boolean {
  return capabilities.includes('codex.login')
}
