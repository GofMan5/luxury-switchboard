import { ControlPlaneError } from '../../../shared/contracts/protocol'
import { appearanceProblem, APPEARANCE_DEFAULTS, normalizeAppearance, type Appearance } from '../domain/appearance'
import type { AppearanceCache, AppearancePort } from './appearance-port'

export interface AppearanceState {
  readonly phase: 'loading' | 'ready' | 'error'
  /** What the window shows: the saved record, or the change in flight. */
  readonly appearance: Appearance
  /** True while a change is on its way to the store. */
  readonly pending: boolean
  readonly error: string
  /** False when the record lives in the browser profile only. */
  readonly durable: boolean
}

/**
 * Appearance changes apply the moment they are made and persist behind the
 * scenes: a theme picker that waits for a round trip reads as broken. Rapid
 * changes (a slider, a colour drag) coalesce into one write of the latest
 * record, and a refused write puts the last saved record back on screen with
 * the reason.
 */
export class AppearanceModel {
  readonly #port: AppearancePort
  readonly #cache: AppearanceCache
  #state: AppearanceState
  #saved: Appearance
  #queued: Appearance | null = null
  #writing = false
  #listeners = new Set<() => void>()
  #unsubscribe: (() => void) | null = null
  #disposed = false

  constructor(port: AppearancePort, cache: AppearanceCache) {
    this.#port = port
    this.#cache = cache
    const cached = cache.read() ?? APPEARANCE_DEFAULTS
    this.#saved = cached
    this.#state = { phase: 'loading', appearance: cached, pending: false, error: '', durable: port.durable }
  }

  snapshot = (): AppearanceState => this.#state
  subscribe = (listener: () => void): (() => void) => {
    this.#listeners.add(listener)
    return () => { this.#listeners.delete(listener) }
  }

  async connect(): Promise<void> {
    this.#unsubscribe ??= this.#port.subscribe((appearance) => {
      // Another window (or a restore) changed the record. A local change in
      // flight wins until it lands; the event after it carries the result.
      if (this.#writing || this.#queued) return
      this.#accept(appearance)
    })
    try {
      const appearance = await this.#port.get()
      if (!this.#disposed && !this.#writing && !this.#queued) this.#accept(appearance)
    } catch {
      if (!this.#disposed) this.#set({ ...this.#state, phase: 'error', error: 'Saved appearance is unavailable; showing the last one this window applied.' })
    }
  }

  /** Applies a change now and persists it. Returns false when the record is
   * invalid; persistence failures surface on the state instead. */
  update(change: Partial<Appearance>): boolean {
    const next = normalizeAppearance({ ...this.#state.appearance, ...change })
    const invalid = appearanceProblem({ ...next, tokens: change.tokens ?? next.tokens, customCss: change.customCss ?? next.customCss })
    if (invalid) {
      this.#set({ ...this.#state, error: invalid })
      return false
    }
    this.#queued = next
    this.#set({ ...this.#state, appearance: next, pending: true, error: '' })
    void this.#flush()
    return true
  }

  reset(): void {
    this.update({ ...APPEARANCE_DEFAULTS })
  }

  dismissError(): void {
    if (this.#state.error) this.#set({ ...this.#state, error: '' })
  }

  async #flush(): Promise<void> {
    if (this.#writing) return
    this.#writing = true
    try {
      while (this.#queued && !this.#disposed) {
        const next = this.#queued
        this.#queued = null
        try {
          const saved = await this.#port.update(next)
          this.#saved = saved
          this.#cache.write(saved)
          if (!this.#queued) this.#set({ ...this.#state, phase: 'ready', appearance: saved, pending: false, error: '' })
        } catch (error) {
          this.#queued = null
          const reason = error instanceof ControlPlaneError ? error.message : 'the store did not answer'
          this.#set({ ...this.#state, appearance: this.#saved, pending: false, error: `Appearance was not saved: ${reason}.` })
        }
      }
    } finally {
      this.#writing = false
    }
  }

  #accept(appearance: Appearance): void {
    this.#saved = appearance
    this.#cache.write(appearance)
    this.#set({ ...this.#state, phase: 'ready', appearance, pending: false, error: '' })
  }

  dispose(): void {
    this.#disposed = true
    this.#unsubscribe?.()
    this.#unsubscribe = null
    this.#listeners.clear()
  }

  #set(state: AppearanceState): void {
    this.#state = state
    for (const listener of this.#listeners) listener()
  }
}
