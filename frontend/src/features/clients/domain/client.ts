export interface TunnelClient {
  readonly ip: string
  readonly actualRpm: number
  readonly count: number
  readonly active: number
  readonly queued: number
  readonly refused: number
  readonly lastSeen: string
  readonly state: string
  readonly banned: boolean
  readonly note: string
}

/** Owner-only governance record for one client address. Never leaves this app. */
export interface TunnelClientProfile {
  readonly ip: string
  readonly banned: boolean
  readonly note: string
}

export const MAX_CLIENT_NOTE = 500

export interface TunnelClientEvent {
  readonly id: string
  readonly ip: string
  readonly time: string
  readonly state: string
  readonly method: string
  readonly path: string
  readonly model: string
  readonly status: number
  readonly latencyMs: number
  readonly bytesIn: number
  readonly bytesOut: number
  readonly errorCode?: string
}
