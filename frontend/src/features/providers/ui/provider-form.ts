import type { ProviderInput } from '../domain/provider'

const authHeaderPattern = /^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,64}$/u
const forbiddenAuthHeaders = new Set([
  'accept', 'accept-encoding', 'connection', 'content-encoding', 'content-length',
  'content-type', 'host', 'proxy-authorization', 'transfer-encoding', 'upgrade',
  'user-agent', 'x-provider-switch-tunnel', 'x-provider-switch-model',
])
const sensitiveQueryNames = new Set([
  'apikey', 'xapikey', 'subscriptionkey', 'key', 'token', 'accesstoken',
  'auth', 'authorization', 'password', 'secret', 'signature', 'sig', 'code',
])

export function providerInputError(value: ProviderInput): string {
  const name = value.name.trim()
  if (!name || [...name].length > 80) return 'Use a provider name up to 80 characters.'
  if (!Number.isInteger(value.rpm) || value.rpm < 0 || value.rpm > 1_000_000) return 'Provider RPM must be an integer from 0 to 1,000,000.'
  if (value.rateUnit !== 'minute' && value.rateUnit !== 'second') return 'Choose whether the request limit is counted per minute or per second.'
  if (!value.modelsPath.startsWith('/') || /[?#\r\n]/u.test(value.modelsPath) || value.modelsPath.length > 160) return 'Models path must start with / and must not contain a query or fragment.'
  if (value.format !== 'auto' && value.format !== 'responses' && value.format !== 'chat') return 'Choose a supported request format.'
  if (value.format === 'chat' && (!value.chatPath.trim().startsWith('/') || /[?#\r\n]/u.test(value.chatPath) || value.chatPath.length > 160)) return 'Chat completions path must start with / and must not contain a query or fragment.'
  if (value.authMode === 'custom') {
    const header = value.authHeader.trim()
    if (!authHeaderPattern.test(header) || forbiddenAuthHeaders.has(header.toLowerCase())) return 'Enter a safe custom authentication header name.'
  }
  let endpoint: URL
  if (value.baseUrl.trim().length > 2_048) return 'Provider URL is too long.'
  try {
    endpoint = new URL(value.baseUrl.trim())
  } catch {
    return 'Enter an absolute provider URL.'
  }
  if ((endpoint.protocol !== 'https:' && endpoint.protocol !== 'http:') || !endpoint.hostname) return 'Provider URL must use HTTPS or loopback HTTP.'
  if (endpoint.username || endpoint.password || endpoint.hash) return 'Keep credentials and fragments out of the provider URL.'
  if (endpoint.protocol === 'http:' && !loopback(endpoint.hostname)) return 'Remote providers must use HTTPS; HTTP is allowed only on loopback.'
  for (const name of endpoint.searchParams.keys()) {
    const normalized = name.toLowerCase().replaceAll(/[-_.]/gu, '')
    if (sensitiveQueryNames.has(normalized)) return 'Put provider credentials in API Keys, not in the URL query.'
  }
  return ''
}

function loopback(hostname: string): boolean {
  const host = hostname.toLowerCase().replace(/^\[(.*)\]$/u, '$1')
  return host === 'localhost' || host === '::1' || /^127(?:\.\d{1,3}){3}$/u.test(host)
}
