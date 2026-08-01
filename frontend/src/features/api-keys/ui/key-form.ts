const proxySchemes = new Set(['http:', 'https:', 'socks5:', 'socks5h:'])

export function proxyURLIsValid(value: string): boolean {
  const raw = value.trim()
  if (!raw) return true
  if (raw.length > 8_192) return false
  try {
    const proxy = new URL(raw)
    return proxySchemes.has(proxy.protocol) && Boolean(proxy.hostname) && !proxy.hash
  } catch {
    return false
  }
}
