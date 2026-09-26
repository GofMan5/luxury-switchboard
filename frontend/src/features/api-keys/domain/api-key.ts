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
  /** Consecutive authentication refusals: three reads as a dead credential. */
  readonly authStreak: number
  /** Outcome of the key's last finished attempt, empty before the first. */
  readonly lastOutcome: string
}

export interface AddApiKey {
  readonly providerId: string
  readonly label: string
  readonly secret: string
  readonly rpm: number
  readonly proxyUrl: string
}

/** One pasted key. The limit and the proxy come from the batch it arrives in. */
export interface ImportApiKeyEntry {
  readonly label: string
  readonly secret: string
}

export interface ImportApiKeys {
  readonly providerId: string
  readonly rpm: number
  readonly proxyUrl: string
  readonly entries: readonly ImportApiKeyEntry[]
}

/** What became of an import, by each entry's position in the submitted batch. */
export interface ImportApiKeysReport {
  readonly added: number
  readonly duplicate: readonly number[]
  readonly rejected: readonly number[]
}

export interface UpdateApiKey {
  readonly providerId: string
  readonly keyId: string
  readonly label: string
  readonly rpm: number
  readonly proxyUrl?: string
  readonly secret?: string
}
