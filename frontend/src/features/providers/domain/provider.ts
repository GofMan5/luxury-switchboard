export interface Provider {
  readonly id: string
  readonly name: string
  readonly baseUrl: string
  readonly authMode: 'passthrough' | 'auto' | 'bearer' | 'x-api-key' | 'custom'
  readonly authHeader: string
  readonly dialect: 'auto' | 'openai' | 'anthropic'
  readonly modelsPath: string
  readonly imageCompat: boolean
  readonly rpm: number
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

export interface ProviderInput {
  readonly name: string
  readonly baseUrl: string
  readonly authMode: Provider['authMode']
  readonly authHeader: string
  readonly dialect: Provider['dialect']
  readonly modelsPath: string
  readonly imageCompat: boolean
  readonly rpm: number
  readonly cache1h: boolean
  readonly enabled: boolean
}
