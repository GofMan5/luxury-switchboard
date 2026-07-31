export interface TunnelClient {
  readonly ip: string
  readonly actualRpm: number
  readonly count: number
  readonly active: number
  readonly queued: number
  readonly lastSeen: string
  readonly state: string
}

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
