import type { EventFrame } from '../../shared/contracts/protocol'

export type EventListener<T = unknown> = (event: EventFrame<T>) => void

export interface ControlPlaneSession {
  start(): Promise<void>
  call<T>(method: string, payload?: unknown, signal?: AbortSignal): Promise<T>
  subscribe<T>(topic: string, listener: EventListener<T>): () => void
  stop(): Promise<void>
  /** Version reported by the handshake, so the interface never states its own. */
  readonly appVersion?: string
}
