export const PROTOCOL_VERSION = 1 as const

export interface CommandFrame {
  readonly v: typeof PROTOCOL_VERSION
  readonly id: string
  readonly type: 'command'
  readonly method: string
  readonly payload?: unknown
}

export interface ResultFrame<T = unknown> {
  readonly v: typeof PROTOCOL_VERSION
  readonly id: string
  readonly type: 'result'
  readonly method?: string
  readonly ok: boolean
  readonly payload?: T
  readonly error?: {
    readonly code: string
    readonly message: string
  }
}

export interface EventFrame<T = unknown> {
  readonly v: typeof PROTOCOL_VERSION
  readonly type: 'event'
  readonly topic: string
  readonly seq: number
  readonly payload?: T
}

export type IncomingFrame = ResultFrame | EventFrame

export class ControlPlaneError extends Error {
  readonly code: string

  constructor(code: string, message: string) {
    super(message)
    this.name = 'ControlPlaneError'
    this.code = code
  }
}
