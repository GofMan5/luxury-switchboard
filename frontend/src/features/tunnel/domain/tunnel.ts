export type TunnelState = 'stopped' | 'starting' | 'online' | 'paused' | 'error'

export interface TunnelSnapshot {
  readonly state: TunnelState
  readonly port: number
  readonly address: string
  readonly rpmPerIp: number
  readonly contextLimitKiB: number
  readonly brandResponse: string
  readonly publisherProfile: string
  readonly tokenConfigured: boolean
  readonly error?: string
}

export interface TunnelConfig {
  readonly port: number
  readonly rpmPerIp: number
  readonly contextLimitKiB: number
  readonly brandResponse: string
  readonly publisherProfile: string
}
