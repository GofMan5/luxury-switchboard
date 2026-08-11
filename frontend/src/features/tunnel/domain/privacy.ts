export interface PrivacyHeader {
  readonly name: string
  readonly values: readonly string[]
}

export interface PrivacyTLS {
  readonly version: string
  readonly cipherSuite: string
  readonly serverName: string
  readonly certificateSubject: string
  readonly certificateIssuer: string
  readonly certificateExpiresAt: string
}

export interface PrivacyModel {
  readonly id: string
  readonly object: string
  readonly created: number
  readonly fields: readonly string[]
}

export interface PrivacyReport {
  readonly checkedAt: string
  readonly requestUrl: string
  readonly status: number
  readonly statusText: string
  readonly protocol: string
  readonly remoteAddress: string
  readonly durationMs: number
  readonly bodyBytes: number
  readonly headers: readonly PrivacyHeader[]
  readonly topLevelFields: readonly string[]
  readonly models: readonly PrivacyModel[]
  readonly rawBody: string
  readonly parseError?: string
  readonly credentialReflected: boolean
  readonly tls?: PrivacyTLS
}

const infrastructureHeaders = /^(?:server|via|x-powered-by|x-provider|x-upstream|x-backend|x-vendor|x-request-id|cf-ray|traceparent)/iu
const privateFields = new Set([
  'ownedby', 'provider', 'providerid', 'providername', 'upstream', 'upstreamurl', 'backend', 'backendid', 'vendor',
  'apikey', 'authorization', 'proxy', 'proxyurl', 'routinghint', 'internal', 'debug', 'systemfingerprint', 'servicetier',
  'endpoint', 'host', 'region', 'deployment', 'cluster', 'node', 'account', 'organization', 'trace', 'traceid', 'server',
])

export function privacyWarnings(report: PrivacyReport): readonly string[] {
  const warnings = new Set<string>()
  if (report.status !== 200) warnings.add(`Endpoint returned ${report.statusText || report.status}`)
  if (report.credentialReflected) warnings.add('The endpoint reflected the access key; its value was redacted')
  for (const header of report.headers) {
    if (infrastructureHeaders.test(header.name)) warnings.add(`Infrastructure header is public: ${header.name}`)
  }
  const fields = [...report.topLevelFields, ...report.models.flatMap((model) => model.fields)]
  for (const field of fields) {
    const normalized = field.replaceAll(/[^\p{L}\p{N}]/gu, '').toLowerCase()
    if (privateFields.has(normalized) || normalized.startsWith('provider') || normalized.startsWith('upstream') || normalized.startsWith('internal') || normalized.startsWith('debug')) {
      warnings.add(`Sensitive-looking response field is public: ${field}`)
    }
  }
  if (/https?:\/\//iu.test(report.rawBody)) warnings.add('The response body contains an absolute URL')
  if (report.parseError) warnings.add(report.parseError)
  return [...warnings]
}
