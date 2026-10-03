import type { UpdateCheck } from '../domain/update'
import type { UpdatesPort } from '../adapters/stdio-updates-port'

const CHECK_INTERVAL_MS = 6 * 60 * 60_000

export interface UpdatesState {
  readonly check: UpdateCheck | null
}

/**
 * The release feed is asked once at startup and then twice a day; the backend
 * caches inside the same window, so this timer costs nothing. An unreachable
 * feed stays invisible — the app works without knowing.
 */
export class UpdatesModel {
  readonly #port: UpdatesPort
  #state: UpdatesState = { check: null }
  #listeners = new Set<() => void>()
  #timer: ReturnType<typeof setInterval> | undefined

  constructor(port: UpdatesPort) { this.#port = port }
  snapshot = (): UpdatesState => this.#state
  subscribe = (listener: () => void): (() => void) => { this.#listeners.add(listener); return () => this.#listeners.delete(listener) }

  connect(): void {
    if (this.#timer !== undefined) return
    void this.check()
    this.#timer = setInterval(() => void this.check(), CHECK_INTERVAL_MS)
  }

  async check(): Promise<void> {
    try {
      const check = await this.#port.check()
      this.#set({ check })
    } catch {
      // A sidecar that cannot answer updates is not an error worth a surface.
    }
  }

  dispose(): void {
    clearInterval(this.#timer)
    this.#timer = undefined
    this.#listeners.clear()
  }

  #set(state: UpdatesState): void {
    this.#state = state
    for (const listener of this.#listeners) listener()
  }
}
