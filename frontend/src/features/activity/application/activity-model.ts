import type { ActivityRequest, ActivitySummary } from '../domain/activity'
import type { ActivityPort } from './activity-port'

export interface ActivityModelState {
  readonly phase: 'loading' | 'ready' | 'error'
  readonly requests: readonly ActivityRequest[]
  /** How many rows the control plane held for this list before the frame budget
   * dropped the oldest ones. Equal to requests.length unless a provider
   * incident made the rows too heavy to ship whole. */
  readonly available: number
  readonly summary: ActivitySummary
  readonly error: string
}

const emptySummary: ActivitySummary = {
  requests: 0,
  active: 0,
  queued: 0,
  successRate: 0,
  p95Ms: 0,
  rpm: 0,
}

export class ActivityModel {
  readonly #port: ActivityPort
  #state: ActivityModelState = {
    phase: 'loading',
    requests: [],
    available: 0,
    summary: emptySummary,
    error: '',
  }
  #listeners = new Set<() => void>()
  #unsubscribe: (() => void) | null = null
  #summaryTimer = 0
  #pollTimer: ReturnType<typeof setInterval> | undefined
  #summaryInFlight = false

  constructor(port: ActivityPort) {
    this.#port = port
  }

  snapshot = (): ActivityModelState => this.#state

  subscribe = (listener: () => void): (() => void) => {
    this.#listeners.add(listener)
    return () => this.#listeners.delete(listener)
  }

  async connect(): Promise<void> {
    const baseline = new Map(this.#state.requests.map((request) => [request.id, request.updatedAt]))
    this.#unsubscribe ??= this.#port.subscribe((request) => this.#accept(request))
    try {
      const [listed, summary] = await Promise.all([this.#port.list(100), this.#port.summary()])
      const live = this.#state.requests.filter((request) => baseline.get(request.id) !== request.updatedAt)
      const liveIDs = new Set(live.map((request) => request.id))
      const merged = [...live, ...listed.requests.filter((request) => !liveIDs.has(request.id))].slice(0, 100)
      this.#set({ phase: 'ready', requests: merged, available: Math.max(listed.available, merged.length), summary, error: '' })
      this.#pollTimer ??= setInterval(() => void this.#refreshSummary(), 1_000)
    } catch {
      this.#set({ ...this.#state, phase: 'error', error: 'Activity is unavailable' })
    }
  }

  dispose(): void {
    this.#unsubscribe?.()
    this.#unsubscribe = null
    window.clearTimeout(this.#summaryTimer)
    clearInterval(this.#pollTimer)
    this.#pollTimer = undefined
    this.#listeners.clear()
  }

  #accept(request: ActivityRequest): void {
    const requests = [request, ...this.#state.requests.filter((item) => item.id !== request.id)].slice(0, 100)
    this.#set({ ...this.#state, phase: 'ready', requests, error: '' })
    window.clearTimeout(this.#summaryTimer)
    this.#summaryTimer = window.setTimeout(() => void this.#refreshSummary(), 250)
  }

  async #refreshSummary(): Promise<void> {
    if (this.#summaryInFlight) return
    this.#summaryInFlight = true
    try {
      const summary = await this.#port.summary()
      const current = this.#state.summary
      if (summary.requests !== current.requests || summary.active !== current.active || summary.queued !== current.queued || summary.successRate !== current.successRate || summary.p95Ms !== current.p95Ms || summary.rpm !== current.rpm) {
        this.#set({ ...this.#state, summary })
      }
    } catch {
      // Live rows remain authoritative; a missed summary refresh is retried on
      // the next event and does not blank the surface.
    } finally {
      this.#summaryInFlight = false
    }
  }

  #set(state: ActivityModelState): void {
    this.#state = state
    for (const listener of this.#listeners) listener()
  }
}
