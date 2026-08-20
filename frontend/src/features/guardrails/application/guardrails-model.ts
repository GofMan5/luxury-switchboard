import type { GuardrailMode, GuardrailRecord, GuardrailStatus } from '../domain/guardrail'
import { isGuardrailMode } from '../domain/guardrail'
import type { GuardrailsPort } from './guardrails-port'

export interface GuardrailsState {
  readonly phase: 'idle' | 'loading' | 'ready' | 'error'
  readonly status: GuardrailStatus | null
  readonly findings: readonly GuardrailRecord[]
  readonly pending: boolean
  readonly error: string
}

/** How many records the workspace holds. The control plane keeps more; this is what
 * one screen can meaningfully show, and it bounds the memory a hostile provider
 * can make the UI hold by answering badly in a loop. */
const VISIBLE_FINDINGS = 200

const EMPTY: GuardrailsState = { phase: 'idle', status: null, findings: [], pending: false, error: '' }

export class GuardrailsModel {
  readonly #port: GuardrailsPort
  #state: GuardrailsState = EMPTY
  #listeners = new Set<() => void>()
  #unsubscribe: (() => void) | null = null
  #unsubscribeStatus: (() => void) | null = null
  #generation = 0

  constructor(port: GuardrailsPort) {
    this.#port = port
  }

  snapshot = () => this.#state

  subscribe = (listener: () => void) => {
    this.#listeners.add(listener)
    return () => this.#listeners.delete(listener)
  }

  async connect() {
    // A finding arriving is prepended directly rather than triggering a reload: the
    // event already carries the whole record, and a provider answering badly in a
    // loop must not turn into a reload storm.
    this.#unsubscribe ??= this.#port.subscribe((record) => this.#prepend(record))
    this.#unsubscribeStatus ??= this.#port.subscribeStatus(() => { void this.refresh() })
    await this.refresh()
  }

  async refresh() {
    const generation = ++this.#generation
    if (this.#state.phase === 'idle') this.#set({ ...this.#state, phase: 'loading' })
    try {
      const [status, findings] = await Promise.all([
        this.#port.status(),
        this.#port.findings(VISIBLE_FINDINGS),
      ])
      if (generation !== this.#generation) return
      this.#set({ ...this.#state, phase: 'ready', status, findings, error: '' })
    } catch {
      if (generation !== this.#generation) return
      this.#set({ ...this.#state, phase: 'error', error: 'The guardrails are unavailable' })
    }
  }

  /** Changes the active mode. It takes effect on the next request, not at restart. */
  async setMode(mode: GuardrailMode): Promise<boolean> {
    if (this.#state.pending) return false
    if (!isGuardrailMode(mode)) {
      this.#set({ ...this.#state, error: 'That inspection mode is not recognised' })
      return false
    }
    if (this.#state.status?.mode === mode) return true
    this.#set({ ...this.#state, pending: true, error: '' })
    try {
      const applied = await this.#port.setMode(mode)
      const status = this.#state.status
      this.#set({
        ...this.#state,
        pending: false,
        error: '',
        status: status ? { ...status, mode: applied } : status,
      })
      return true
    } catch {
      this.#set({ ...this.#state, pending: false, error: 'The inspection mode could not be saved' })
      return false
    }
  }

  async clear(): Promise<boolean> {
    if (this.#state.pending) return false
    this.#set({ ...this.#state, pending: true, error: '' })
    try {
      await this.#port.clear()
      const status = this.#state.status
      this.#set({
        ...this.#state,
        pending: false,
        findings: [],
        error: '',
        status: status ? { ...status, findingCount: 0 } : status,
      })
      return true
    } catch {
      this.#set({ ...this.#state, pending: false, error: 'The findings could not be cleared' })
      return false
    }
  }

  clearError() {
    if (this.#state.error) this.#set({ ...this.#state, error: '' })
  }

  dispose() {
    this.#unsubscribe?.()
    this.#unsubscribeStatus?.()
    this.#unsubscribe = null
    this.#unsubscribeStatus = null
    this.#listeners.clear()
  }

  #prepend(record: GuardrailRecord) {
    if (!record?.id) return
    // The same record can arrive twice across a reconnect, and a duplicate would
    // read as a second attack rather than one.
    if (this.#state.findings.some((existing) => existing.id === record.id)) return
    const findings = [record, ...this.#state.findings].slice(0, VISIBLE_FINDINGS)
    const status = this.#state.status
    this.#set({
      ...this.#state,
      phase: 'ready',
      findings,
      status: status ? { ...status, findingCount: status.findingCount + 1 } : status,
    })
  }

  #set(state: GuardrailsState) {
    this.#state = state
    for (const listener of this.#listeners) listener()
  }
}
