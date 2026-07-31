export type RelayState = 'starting' | 'live' | 'stopped' | 'error'

export interface RelaySnapshot {
  readonly state: RelayState
  readonly address: string
  readonly port: number
  readonly error?: string
}
