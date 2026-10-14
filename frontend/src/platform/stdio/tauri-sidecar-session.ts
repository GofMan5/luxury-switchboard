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

// Match the sidecar/shell byte budgets, not JavaScript UTF-16 lengths.
const MAX_FRAME_BYTES = 262_144
const MAX_PAYLOAD_BYTES = 258_048
const CALL_TIMEOUT_MS = 30_000
// Pool checks return a partial report at their two-minute backend deadline.
const KEYS_CHECK_TIMEOUT_MS = 130_000
const SHUTDOWN_CALL_TIMEOUT_MS = 2_000
const SHUTDOWN_EXIT_TIMEOUT_MS = 10_000
const TUNNEL_START_TIMEOUT_MS = 60_000
// The quota probe is a backend chain, not one request: up to one token
// refresh (25s) before each attempt and a second full probe after a 401,
// i.e. 25+20+25+20 = 90s designed worst case. Cutting it earlier would
// misreport a slow-but-healthy upstream as a dead relay and cancel the
// still-flying probe.
const CODEX_QUOTA_TIMEOUT_MS = 90_000
// The fleet probe is one quota chain per account, but the backend answers
// every account in parallel under one call, so the single probe's worst case
// is also the fleet's: 90s, not 90s times N.
const CODEX_QUOTA_ALL_TIMEOUT_MS = 90_000
// The wake is the same chain minus the usage read: one optional token
// refresh per account, run in parallel, so the single-probe budget covers
// it with room to spare.
const CODEX_WAKE_TIMEOUT_MS = 90_000
const MODEL_DISCOVERY_TIMEOUT_MS = 2 * 60_000
const MODEL_TEST_TIMEOUT_MS = 3 * 60_000
// The installer download is the longest command the control plane runs: the
// backend gives it fifteen minutes, and the shell must not cut it shorter —
// a timeout here cancels the sidecar's download and deletes the partial file.
const UPDATE_INSTALL_TIMEOUT_MS = 16 * 60_000

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
  #restartTimer: number | undefined
  #connectedOnce = false
  #disconnectWaiters = new Set<() => void>()
  #appVersion = ''
  #capabilities: readonly string[] = []

  /** Reported by the handshake, so the interface never states a version of its own. */
  get appVersion(): string {
    return this.#appVersion
  }

  /** Commands the control plane advertised in the handshake; empty when absent. */
  get capabilities(): readonly string[] {
    return this.#capabilities
  }

  start(): Promise<void> {
    if (this.#started) return Promise.resolve()
    if (this.#starting) return this.#starting
    this.#starting = this.#spawn().finally(() => {
      this.#starting = null
    })
    return this.#starting
  }

  async call<T>(method: string, payload?: unknown, signal?: AbortSignal): Promise<T> {
    if (signal?.aborted) throw new DOMException('Sidecar command aborted', 'AbortError')
    await this.start()
    if (signal?.aborted) throw new DOMException('Sidecar command aborted', 'AbortError')
    if (!this.#started) throw new ControlPlaneError('not_connected', 'Sidecar is not connected')
    const id = requestID()
    const frame: CommandFrame = {
      v: PROTOCOL_VERSION,
      id,
      type: 'command',
      method,
      ...(payload === undefined ? {} : { payload }),
    }
    const cancel = () => {
      void this.#write({
        v: PROTOCOL_VERSION,
        id: requestID(),
        type: 'command',
        method: 'system.cancel',
        payload: { id },
      }).catch(() => undefined)
    }
    const result = new Promise<T>((resolve, reject) => {
      const timeout = window.setTimeout(() => {
        this.#pending.delete(id)
        cancel()
        reject(new ControlPlaneError('timeout', 'Sidecar command timed out'))
      }, commandTimeout(method))
      this.#pending.set(id, {
        method,
        resolve: (value) => resolve(value as T),
        reject,
        timeout,
      })
    })
    const abort = () => {
      const pending = this.#pending.get(id)
      if (!pending) return
      this.#pending.delete(id)
      window.clearTimeout(pending.timeout)
      cancel()
      pending.reject(new DOMException('Sidecar command aborted', 'AbortError'))
    }
    signal?.addEventListener('abort', abort, { once: true })
    if (signal?.aborted) abort()
    // The terminal promise is observed immediately, independently of native
    // pipe backpressure. Late send failures cannot replace an already settled call.
    void this.#write(frame).catch((error: unknown) => {
      const pending = this.#pending.get(id)
      if (!pending) return
      this.#pending.delete(id)
      window.clearTimeout(pending.timeout)
      pending.reject(error instanceof Error ? error : new Error('Sidecar command could not be sent'))
    })
    try {
      return await result
    } finally {
      signal?.removeEventListener('abort', abort)
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
    window.clearTimeout(this.#restartTimer)
    this.#restartTimer = undefined
    if (this.#starting) {
      try { await this.#starting } catch { /* the failed start is cleaned below */ }
    }
    if (this.#started) {
      const disconnected = this.#waitForDisconnect(SHUTDOWN_EXIT_TIMEOUT_MS)
      try { await this.call('system.shutdown') } catch { /* hard-stop fallback remains below */ }
      await disconnected
    }
    this.#started = false
    this.#rejectPending(new ControlPlaneError('disconnected', 'Sidecar stopped'))
    try {
      await invoke('sidecar_stop')
    } finally {
      for (const unlisten of this.#unlisten.splice(0)) unlisten()
    }
  }

  async #spawn(): Promise<void> {
    this.#stopping = false
    if (this.#unlisten.length === 0) {
      const unlistenFrame = await listen<string>('sidecar-frame', (event) => this.#consume(event.payload))
      try {
        const unlistenLifecycle = await listen('sidecar-lifecycle', () => this.#disconnect())
        this.#unlisten.push(unlistenFrame, unlistenLifecycle)
      } catch (error) {
        unlistenFrame()
        throw error
      }
    }
    try {
      await invoke('sidecar_start')
      this.#started = true
      const handshake = await this.call<{ appVersion?: unknown; capabilities?: unknown }>('system.handshake')
      const version = handshake?.appVersion
      this.#appVersion = typeof version === 'string' && /^\d{1,4}(\.\d{1,4}){1,3}$/u.test(version) ? version : ''
      this.#capabilities = Array.isArray(handshake?.capabilities)
        ? handshake.capabilities.filter((entry): entry is string => typeof entry === 'string')
        : []
      if (this.#connectedOnce) {
        this.#publish({ v: PROTOCOL_VERSION, type: 'event', topic: 'system.reconnected', seq: 0 })
      }
      this.#connectedOnce = true
    } catch (error) {
      this.#started = false
      const stopping = this.#stopping
      this.#stopping = true
      try { await invoke('sidecar_stop') } catch { /* retain the original start error */ }
      this.#stopping = stopping
      throw error
    }
  }

  async #write(frame: CommandFrame): Promise<void> {
    if (!this.#started) throw new ControlPlaneError('not_connected', 'Sidecar is not connected')
    const encoded = JSON.stringify(frame)
    const encoder = new TextEncoder()
    if (encoder.encode(encoded).byteLength > MAX_FRAME_BYTES || (frame.payload !== undefined && encoder.encode(JSON.stringify(frame.payload)).byteLength > MAX_PAYLOAD_BYTES)) {
      throw new ControlPlaneError('command_too_large', frame.method === 'keys.addMany'
        ? 'This import is too large for one command. Split it into smaller batches; no keys were sent.'
        : 'This command is too large to send. Reduce its input and try again.')
    }
    await invoke('sidecar_write', { frame: encoded })
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
    for (const resolve of [...this.#disconnectWaiters]) resolve()
    if (!this.#stopping) this.#scheduleRestart()
  }

  #scheduleRestart(): void {
    if (this.#restartTimer !== undefined || this.#stopping) return
    this.#restartTimer = window.setTimeout(() => {
      this.#restartTimer = undefined
      if (this.#stopping) return
      void this.start().catch(() => this.#scheduleRestart())
    }, 1_000)
  }

  #waitForDisconnect(timeoutMs: number): Promise<void> {
    return new Promise((resolve) => {
      let timer = 0
      const done = () => {
        window.clearTimeout(timer)
        this.#disconnectWaiters.delete(done)
        resolve()
      }
      this.#disconnectWaiters.add(done)
      timer = window.setTimeout(done, timeoutMs)
    })
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

function commandTimeout(method: string): number {
  if (method === 'keys.check') return KEYS_CHECK_TIMEOUT_MS
  if (method === 'models.discover') return MODEL_DISCOVERY_TIMEOUT_MS
  if (method === 'models.test') return MODEL_TEST_TIMEOUT_MS
  if (method === 'tunnel.start') return TUNNEL_START_TIMEOUT_MS
  if (method === 'codex.quota') return CODEX_QUOTA_TIMEOUT_MS
  if (method === 'codex.quota.all') return CODEX_QUOTA_ALL_TIMEOUT_MS
  if (method === 'codex.wake') return CODEX_WAKE_TIMEOUT_MS
  if (method === 'system.shutdown') return SHUTDOWN_CALL_TIMEOUT_MS
  if (method === 'updates.install') return UPDATE_INSTALL_TIMEOUT_MS
  return CALL_TIMEOUT_MS
}
