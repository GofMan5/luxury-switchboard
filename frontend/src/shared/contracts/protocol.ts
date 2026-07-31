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

const requestId = /^[A-Za-z0-9_-]{1,80}$/u

export function decodeIncomingFrame(text: string): IncomingFrame {
  const value: unknown = JSON.parse(text)
  if (!value || typeof value !== 'object') throw new Error('Sidecar returned an invalid protocol frame')
  const frame = value as Record<string, unknown>
  if (frame.v !== PROTOCOL_VERSION) throw new Error('Sidecar returned an invalid protocol frame')
  if (frame.type === 'result' && typeof frame.id === 'string' && requestId.test(frame.id) && typeof frame.ok === 'boolean') {
    if (!frame.ok && !validError(frame.error)) throw new Error('Sidecar returned an invalid protocol frame')
    return frame as unknown as ResultFrame
  }
  if (frame.type === 'event' && typeof frame.topic === 'string' && frame.topic.length > 0 && frame.topic.length <= 120 && Number.isSafeInteger(frame.seq) && Number(frame.seq) >= 0) {
    return frame as unknown as EventFrame
  }
  throw new Error('Sidecar returned an invalid protocol frame')
}

function validError(value: unknown): boolean {
  if (!value || typeof value !== 'object') return false
  const error = value as Record<string, unknown>
  return typeof error.code === 'string' && error.code.length > 0 && typeof error.message === 'string'
}

export class ControlPlaneError extends Error {
  readonly code: string

  constructor(code: string, message: string) {
    super(message)
    this.name = 'ControlPlaneError'
    this.code = code
  }
}
