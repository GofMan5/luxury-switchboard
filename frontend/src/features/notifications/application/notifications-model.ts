import type { Notification, NotificationsPort } from '../adapters/stdio-notifications-port'

export interface NotificationsState {
  readonly phase: 'loading' | 'ready' | 'error'
  readonly notifications: readonly Notification[]
  /** The bounded live stack; expiry publishes a new external-store snapshot. */
  readonly toasts: readonly Notification[]
  /** Notifications the operator has not seen yet; the badge counts these. */
  readonly unread: number
  readonly enabled: boolean
  readonly error: string
}

/** How long a toast is fully on screen. The exit animation plays on top of
 * this, driven by CSS, so the model holds the row for the animation's sake
 * and retires it a beat after. Hover pauses nothing on purpose: the feed
 * keeps every notification, so a missed toast is one click away, and a hover
 * trap fights the pointer. */
const TOAST_TTL_MS = 5_000
/** The exit animation's duration; the model waits it out before dropping the
 * row so the animation is never cut mid-frame. */
const TOAST_EXIT_MS = 300
/** How many toasts stack before the oldest is dropped outright. */
const MAX_VISIBLE_TOASTS = 4

export class NotificationsModel {
  readonly #port: NotificationsPort
  #state: NotificationsState = { phase: 'loading', notifications: [], toasts: [], unread: 0, enabled: true, error: '' }
  #listeners = new Set<() => void>()
  #unsubscribe: (() => void) | null = null
  #timers = new Set<ReturnType<typeof setTimeout>>()
  #localSeq = 0

  constructor(port: NotificationsPort) {
    this.#port = port
  }

  snapshot = (): NotificationsState => this.#state

  subscribe = (listener: () => void): (() => void) => {
    this.#listeners.add(listener)
    return () => this.#listeners.delete(listener)
  }

  /** Toasts the notifications currently on screen; the renderer owns their
   * lifetime through expiry, so a re-render does not reset any timer. */
  toasts(): readonly Notification[] {
    if (!this.#state.enabled) return []
    return this.#state.toasts
  }

  async connect(): Promise<void> {
    this.#unsubscribe ??= this.#port.subscribe((notification) => this.#accept(notification))
    try {
      const notifications = await this.#port.list()
      this.#set({ ...this.#state, phase: 'ready', notifications, unread: 0, error: '' })
    } catch {
      this.#set({ ...this.#state, phase: 'error', error: 'Notifications are unavailable' })
    }
  }

  dispose(): void {
    this.#unsubscribe?.()
    this.#unsubscribe = null
    for (const timer of this.#timers) clearTimeout(timer)
    this.#timers.clear()
    this.#listeners.clear()
  }

  markRead(): void {
    if (this.#state.unread !== 0) this.#set({ ...this.#state, unread: 0 })
  }

  setEnabled(enabled: boolean): void {
    if (this.#state.enabled !== enabled) this.#set({ ...this.#state, enabled })
  }

  /** Raises a notification that originates in this window (a plugin's
   * `notify`, for one) rather than in the control plane. It takes the same
   * path as a sidecar notification: the feed, the badge, the master switch
   * and the toast lifetime all apply. Window-local: it is not persisted, and
   * a reload forgets it. */
  announce(input: Omit<Notification, 'id' | 'at'>): void {
    this.#localSeq += 1
    this.#accept({ ...input, id: `local-${Date.now().toString(36)}-${this.#localSeq}`, at: new Date().toISOString() })
  }

  async clear(): Promise<void> {
    try {
      await this.#port.clear()
      for (const timer of this.#timers) clearTimeout(timer)
      this.#timers.clear()
      this.#set({ ...this.#state, notifications: [], toasts: [], unread: 0 })
    } catch {
      this.#set({ ...this.#state, error: 'The feed could not be cleared' })
    }
  }

  #accept(notification: Notification): void {
    if (!notification?.id) return
    if (this.#state.notifications.some((existing) => existing.id === notification.id)) return
    const notifications = [notification, ...this.#state.notifications].slice(0, 200)
    this.#set({ ...this.#state, phase: 'ready', notifications, error: '',
      toasts: this.#state.enabled ? [notification, ...this.#state.toasts].slice(0, MAX_VISIBLE_TOASTS) : this.#state.toasts,
      unread: this.#state.unread + (this.#state.enabled ? 1 : 0),
    })
    if (this.#state.enabled) {
      // The toast retires itself; the notification itself stays in the feed.
      // The timer fires after the exit animation finished playing, so the row
      // leaves the stack only once it is visually gone.
      const timer = setTimeout(() => {
        this.#timers.delete(timer)
        this.#set({ ...this.#state, toasts: this.#state.toasts.filter((toast) => toast.id !== notification.id) })
      }, TOAST_TTL_MS + TOAST_EXIT_MS)
      this.#timers.add(timer)
    }
  }

  #set(state: NotificationsState): void {
    this.#state = state
    this.#notify()
  }

  #notify(): void {
    for (const listener of this.#listeners) listener()
  }
}
