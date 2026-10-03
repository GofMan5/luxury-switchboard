import type { Settings } from '../domain/settings'
import type { SettingsPort } from './settings-port'
import { ControlPlaneError } from '../../../shared/contracts/protocol'

export interface SettingsState {
  readonly phase: 'loading' | 'ready' | 'error'
  readonly settings: Settings | null
  readonly pending: boolean
  readonly error: string
}

export class SettingsModel {
  readonly #port: SettingsPort
  #state: SettingsState = { phase: 'loading', settings: null, pending: false, error: '' }
  #listeners = new Set<() => void>()
  #unsubscribe: (() => void) | null = null
  #generation = 0

  constructor(port: SettingsPort) { this.#port = port }
  snapshot = (): SettingsState => this.#state
  subscribe = (listener: () => void): (() => void) => { this.#listeners.add(listener); return () => this.#listeners.delete(listener) }

  async connect(): Promise<void> {
    const generation = ++this.#generation
    this.#unsubscribe ??= this.#port.subscribe((result) => {
      this.#generation++
      this.#set({ phase: 'ready', settings: result.settings, pending: this.#state.pending, error: '' })
    })
    try {
      const settings = await this.#port.get()
      if (generation === this.#generation) this.#set({ phase: 'ready', settings, pending: false, error: '' })
    } catch {
      if (generation === this.#generation) this.#set({ ...this.#state, phase: 'error', error: 'Settings are unavailable' })
    }
  }

  async save(settings: Settings): Promise<boolean> {
    if (this.#state.pending) return false
    this.#generation++
    this.#set({ ...this.#state, pending: true, error: '' })
    try {
      const result = await this.#port.update(settings)
      this.#set({ phase: 'ready', settings: result.settings, pending: false, error: '' })
      return true
    } catch (error) {
      // The control plane names the field that failed validation ("listener
      // port is out of range") — the banner repeats it instead of a shrug.
      const message = error instanceof ControlPlaneError ? `Settings could not be saved: ${error.message}.` : 'Settings could not be saved. Check every value.'
      this.#set({ ...this.#state, pending: false, error: message })
      return false
    }
  }

  dispose(): void { this.#unsubscribe?.(); this.#listeners.clear() }
  #set(state: SettingsState): void { this.#state = state; for (const listener of this.#listeners) listener() }
}
