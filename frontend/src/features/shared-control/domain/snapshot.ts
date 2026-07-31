export interface SharedTunnel {
  readonly position: number
  readonly name: string
  readonly state: 'running' | 'paused' | 'stopped'
}

export interface SharedSnapshot {
  readonly available: boolean
  readonly revision: number
  readonly tunnels: readonly SharedTunnel[]
  readonly error: string
  readonly stale?: boolean
}

export type SharedAction = 'pause' | 'resume' | 'stop'
