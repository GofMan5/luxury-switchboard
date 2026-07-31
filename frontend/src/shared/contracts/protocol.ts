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

export function decodeIncomingFrame(text: string): IncomingFrame {
  const value: unknown = JSON.parse(text)
  if (!value || typeof value !== 'object') throw new Error('Sidecar returned an invalid protocol frame')
  const frame = value as Record<string, unknown>
  if (frame.v !== PROTOCOL_VERSION) throw new Error('Sidecar returned an invalid protocol frame')
  if (frame.type === 'result' && typeof frame.id === 'string' && typeof frame.ok === 'boolean') return frame as unknown as ResultFrame
  if (frame.type === 'event' && typeof frame.topic === 'string' && Number.isSafeInteger(frame.seq)) return frame as unknown as EventFrame
  throw new Error('Sidecar returned an invalid protocol frame')
}

export class ControlPlaneError extends Error {
  readonly code: string

  constructor(code: string, message: string) {
    super(message)
    this.name = 'ControlPlaneError'
    this.code = code
  }
}
