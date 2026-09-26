// RateUnit is the period a provider's request limit is counted over. Most
// publish a per-minute quota, but some cap bursts per second.
export type RateUnit = 'minute' | 'second'

export interface Provider {
  readonly id: string
  readonly name: string
  readonly baseUrl: string
  readonly authMode: 'passthrough' | 'auto' | 'bearer' | 'x-api-key' | 'custom'
  readonly authHeader: string
  readonly dialect: 'auto' | 'openai' | 'anthropic'
  readonly modelsPath: string
  readonly format: 'auto' | 'responses' | 'chat'
  readonly chatPath: string
  readonly imageCompat: boolean
  readonly rpm: number
  readonly rateUnit: RateUnit
  readonly cacheTtl: string
  readonly enabled: boolean
  readonly keyConfigured: boolean
  readonly keyCount: number
  readonly builtin: boolean
}

export interface ProviderCatalog {
  readonly activeId: string
  readonly providers: readonly Provider[]
}

/** One provider's reachability, as the sidebar dot and provider rows show it.
 * A missing entry means "not probed yet": the probe runs every two minutes. */
export interface ProviderHealth {
  readonly providerId: string
  readonly up: boolean
  readonly reason: string
}

export interface ProviderInput {
  readonly name: string
  readonly baseUrl: string
  readonly authMode: Provider['authMode']
  readonly authHeader: string
  readonly dialect: Provider['dialect']
  readonly modelsPath: string
  readonly format: Provider['format']
  readonly chatPath: string
  readonly imageCompat: boolean
  readonly rpm: number
  readonly rateUnit: RateUnit
  readonly cache1h: boolean
  readonly enabled: boolean
}
