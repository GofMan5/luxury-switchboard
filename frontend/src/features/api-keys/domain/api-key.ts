export interface ApiKey {
  readonly id: string
  readonly providerId: string
  readonly label: string
  readonly priority: number
  readonly rpm: number
  readonly pinned: boolean
  readonly proxyConfigured: boolean
  readonly cooldownMs: number
  readonly blockedModels: number
  readonly retries429: number
  readonly startsInWindow: number
}

export interface AddApiKey {
  readonly providerId: string
  readonly label: string
  readonly secret: string
  readonly rpm: number
  readonly proxyUrl: string
}

export interface UpdateApiKey {
  readonly providerId: string
  readonly keyId: string
  readonly label: string
  readonly rpm: number
  readonly proxyUrl?: string
  readonly secret?: string
}
