import { invoke } from '@tauri-apps/api/core'
import { listen, type UnlistenFn } from '@tauri-apps/api/event'
import {
  ControlPlaneError,
  decodeIncomingFrame,
  PROTOCOL_VERSION,
  type CommandFrame,
  type EventFrame,
  type ResultFrame,
} from '../../shared/contracts/protocol'
import type { ControlPlaneSession, EventListener } from './session'

const CALL_TIMEOUT_MS = 30_000
const MODEL_TEST_TIMEOUT_MS = 15 * 60_000

interface PendingCall {
  readonly method: string
  readonly resolve: (payload: unknown) => void
  readonly reject: (error: Error) => void
  readonly timeout: number
}

export class TauriSidecarSession implements ControlPlaneSession {
  #started = false
  #pending = new Map<string, PendingCall>()
  #listeners = new Map<string, Set<EventListener>>()
  #starting: Promise<void> | null = null
  #stopping = false
  #unlisten: UnlistenFn[] = []

  start(): Promise<void> {
    if (this.#started) return Promise.resolve()
    if (this.#starting) return this.#starting
    this.#starting = this.#spawn().finally(() => {
      this.#starting = null
    })
    return this.#starting
  }

  async call<T>(method: string, payload?: unknown, signal?: AbortSignal): Promise<T> {
    await this.start()
    if (!this.#started) throw new ControlPlaneError('not_connected', 'Sidecar is not connected')
    const id = requestID()
    const frame: CommandFrame = {
      v: PROTOCOL_VERSION,
      id,
      type: 'command',
      method,
      ...(payload === undefined ? {} : { payload }),
    }
    const result = new Promise<T>((resolve, reject) => {
      const timeout = window.setTimeout(() => {
        this.#pending.delete(id)
        reject(new ControlPlaneError('timeout', 'Sidecar command timed out'))
      }, method === 'models.test' ? MODEL_TEST_TIMEOUT_MS : CALL_TIMEOUT_MS)
      this.#pending.set(id, {
        method,
        resolve: (value) => resolve(value as T),
        reject,
        timeout,
      })
    })
    const cancel = () => {
      void this.#write({
        v: PROTOCOL_VERSION,
        id: requestID(),
        type: 'command',
        method: 'system.cancel',
        payload: { id },
      })
    }
    signal?.addEventListener('abort', cancel, { once: true })
    try {
      await this.#write(frame)
      return await result
    } finally {
      signal?.removeEventListener('abort', cancel)
    }
  }

  subscribe<T>(topic: string, listener: EventListener<T>): () => void {
    const listeners = this.#listeners.get(topic) ?? new Set<EventListener>()
    listeners.add(listener as EventListener)
    this.#listeners.set(topic, listeners)
    return () => {
      listeners.delete(listener as EventListener)
      if (listeners.size === 0) this.#listeners.delete(topic)
    }
  }

  async stop(): Promise<void> {
    this.#stopping = true
    this.#started = false
    this.#rejectPending(new ControlPlaneError('disconnected', 'Sidecar stopped'))
    await invoke('sidecar_stop')
    for (const unlisten of this.#unlisten.splice(0)) unlisten()
  }

  async #spawn(): Promise<void> {
    this.#stopping = false
    if (this.#unlisten.length === 0) {
      this.#unlisten.push(
        await listen<string>('sidecar-frame', (event) => this.#consume(event.payload)),
        await listen('sidecar-lifecycle', () => this.#disconnect()),
      )
    }
    await invoke('sidecar_start')
    this.#started = true
    await this.call('system.handshake')
  }

  async #write(frame: CommandFrame): Promise<void> {
    if (!this.#started) throw new ControlPlaneError('not_connected', 'Sidecar is not connected')
    await invoke('sidecar_write', { frame: JSON.stringify(frame) })
  }

  #consume(chunk: string): void {
    let frame: ResultFrame | EventFrame
    try {
      frame = decodeIncomingFrame(chunk)
    } catch {
      this.#disconnect()
      return
    }
    if (frame.type === 'result') this.#resolve(frame)
    else this.#publish(frame)
  }

  #resolve(frame: ResultFrame): void {
    const pending = this.#pending.get(frame.id)
    if (!pending) return
    this.#pending.delete(frame.id)
    window.clearTimeout(pending.timeout)
    if (frame.ok) pending.resolve(frame.payload)
    else {
      pending.reject(
        new ControlPlaneError(
          frame.error?.code ?? 'command_failed',
          frame.error?.message ?? `${pending.method} failed`,
        ),
      )
    }
  }

  #publish(frame: EventFrame): void {
    for (const listener of this.#listeners.get(frame.topic) ?? []) listener(frame)
  }

  #disconnect(): void {
    this.#started = false
    this.#rejectPending(new ControlPlaneError('disconnected', 'Sidecar disconnected'))
    if (!this.#stopping) {
      window.setTimeout(() => void this.start(), 1_000)
    }
  }

  #rejectPending(error: Error): void {
    for (const pending of this.#pending.values()) {
      window.clearTimeout(pending.timeout)
      pending.reject(error)
    }
    this.#pending.clear()
  }
}

function requestID(): string {
  return `req_${crypto.randomUUID().replaceAll('-', '')}`
}
