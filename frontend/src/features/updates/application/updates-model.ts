import type { UpdateCheck } from '../domain/update'
import type { UpdatesPort } from './updates-port'

export interface UpdatesState {
  readonly check: UpdateCheck | null
  /** A manual check is in flight: the button in Settings says so and stops
   * taking clicks. The automatic passes never set this — they are not the
   * operator's transaction to watch. */
  readonly checking: boolean
  /** Why the last manual check could not answer. The automatic passes stay
   * silent — an unreachable feed between checks is not worth a surface —
   * but an operator who pressed the button is owed the sentence. */
  readonly checkError: string
  readonly installPhase: 'idle' | 'downloading' | 'verifying' | 'ready' | 'error'
  readonly installPercent: number
  readonly installerPath: string
  readonly installError: string
}

/**
 * The backend owns freshness: a refresher asks the release feed on the
 * interval Settings chose, backs off when the feed fails, and announces every
 * completed answer as an event. This model holds no timer of its own — it
 * joins that flow of events, so the pill that says "update available" turns
 * on the minute the verdict flips, not at the next poll the interface happens
 * to make. A manual check is the same single-flighted round trip the refresher
 * makes: one already in flight is joined, not stacked with a second.
 *
 * The install keeps its honesty: the backend downloads and verifies against
 * the release's own checksum before anything is said to be ready, and the
 * shell's run-installer command is the only thing that ever executes the file.
 */
export class UpdatesModel {
  readonly #port: UpdatesPort
  #state: UpdatesState = { check: null, checking: false, checkError: '', installPhase: 'idle', installPercent: 0, installerPath: '', installError: '' }
  #listeners = new Set<() => void>()
  #connected = false
  #disposed = false
  #checking = false
  #installing = false
  #unsubscribeState: (() => void) | null = null
  #unsubscribeProgress: (() => void) | null = null

  constructor(port: UpdatesPort) { this.#port = port }
  snapshot = (): UpdatesState => this.#state
  subscribe = (listener: () => void): (() => void) => { this.#listeners.add(listener); return () => this.#listeners.delete(listener) }

  connect(): void {
    if (this.#connected || this.#disposed) return
    this.#connected = true
    // Seed from the backend's own answer — the refresher has usually checked
    // before the interface even asked — then stay fresh on events alone.
    void this.#port.status().then((check) => {
      if (check && !this.#disposed) this.#set({ ...this.#state, check })
    }).catch(() => {
      // A sidecar that cannot answer yet is not an error worth a surface:
      // the first stateChanged event brings the same answer.
    })
    this.#unsubscribeState ??= this.#port.onStateChanged((check) => {
      this.#set({ ...this.#state, check })
    })
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

  /** Asks the feed now. A click while one runs does not start a second round
   * trip — the backend single-flights, and the first click's answer serves
   * both. */
  async check(): Promise<void> {
    if (this.#checking) return
    this.#checking = true
    this.#set({ ...this.#state, checking: true, checkError: '' })
    try {
      const check = await this.#port.check()
      this.#set({ ...this.#state, check, checking: false })
    } catch (error) {
      this.#set({
        ...this.#state,
        checking: false,
        checkError: error instanceof Error ? error.message : 'The release feed could not be reached',
      })
    } finally {
      this.#checking = false
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
    this.#disposed = true
    this.#unsubscribeState?.()
    this.#unsubscribeState = null
    this.#unsubscribeProgress?.()
    this.#unsubscribeProgress = null
    this.#listeners.clear()
  }

  #set(state: UpdatesState): void {
    this.#state = state
    for (const listener of this.#listeners) listener()
  }
}
