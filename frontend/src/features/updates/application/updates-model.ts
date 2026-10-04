import type { UpdateCheck } from '../domain/update'
import type { UpdatesPort } from '../adapters/stdio-updates-port'

const CHECK_INTERVAL_MS = 6 * 60 * 60_000

export interface UpdatesState {
  readonly check: UpdateCheck | null
  readonly installPhase: 'idle' | 'downloading' | 'verifying' | 'ready' | 'error'
  readonly installPercent: number
  readonly installerPath: string
  readonly installError: string
}

/**
 * The release feed is asked once at startup and then twice a day; the backend
 * caches inside the same window, so this timer costs nothing. An unreachable
 * feed stays invisible — the app works without knowing.
 *
 * The install is the same honesty: the backend downloads and verifies against
 * the release's own checksum before anything is said to be ready, and the
 * shell's run-installer command is the only thing that ever executes the file.
 */
export class UpdatesModel {
  readonly #port: UpdatesPort
  #state: UpdatesState = { check: null, installPhase: 'idle', installPercent: 0, installerPath: '', installError: '' }
  #listeners = new Set<() => void>()
  #timer: ReturnType<typeof setInterval> | undefined
  #unsubscribeProgress: (() => void) | null = null
  #installing = false

  constructor(port: UpdatesPort) { this.#port = port }
  snapshot = (): UpdatesState => this.#state
  subscribe = (listener: () => void): (() => void) => { this.#listeners.add(listener); return () => this.#listeners.delete(listener) }

  connect(): void {
    if (this.#timer !== undefined) return
    void this.check()
    this.#timer = setInterval(() => void this.check(), CHECK_INTERVAL_MS)
    this.#unsubscribeProgress ??= this.#port.onInstallProgress((report) => {
      // The ready event still precedes the command's answer: the file is
      // verified, the path is about to arrive, and "verifying" is the last
      // honest label until it does.
      this.#set({
        ...this.#state,
        installPhase: report.phase === 'downloading' ? 'downloading' : 'verifying',
        installPercent: report.percent,
      })
    })
  }

  async check(): Promise<void> {
    try {
      const check = await this.#port.check()
      this.#set({ ...this.#state, check })
    } catch {
      // A sidecar that cannot answer updates is not an error worth a surface.
    }
  }

  /** Runs the verified download. True leaves a ready installer at
   * installerPath; false leaves the error said out loud. */
  async install(): Promise<boolean> {
    if (this.#installing) return false
    this.#installing = true
    this.#set({ ...this.#state, installPhase: 'downloading', installPercent: 0, installerPath: '', installError: '' })
    try {
      const install = await this.#port.install()
      this.#set({ ...this.#state, installPhase: 'ready', installPercent: 100, installerPath: install.path })
      return true
    } catch (error) {
      this.#set({
        ...this.#state,
        installPhase: 'error',
        installError: error instanceof Error ? error.message : 'The update could not be downloaded',
      })
      return false
    } finally {
      this.#installing = false
    }
  }

  dispose(): void {
    clearInterval(this.#timer)
    this.#timer = undefined
    this.#unsubscribeProgress?.()
    this.#unsubscribeProgress = null
    this.#listeners.clear()
  }

  #set(state: UpdatesState): void {
    this.#state = state
    for (const listener of this.#listeners) listener()
  }
}
