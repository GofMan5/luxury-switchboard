import type { Notification, NotificationsPort } from '../adapters/stdio-notifications-port'

export interface NotificationsState {
  readonly phase: 'loading' | 'ready' | 'error'
  readonly notifications: readonly Notification[]
  /** Notifications the operator has not seen yet; the badge counts these. */
  readonly unread: number
  readonly enabled: boolean
  readonly error: string
}

/** How long a toast stays on screen. Hover pauses nothing on purpose: the feed
 * keeps every notification, so a missed toast is one click away, and a hover
 * trap fights the pointer. */
const TOAST_TTL_MS = 6_000
/** How many toasts stack before the oldest is dropped outright. */
const MAX_VISIBLE_TOASTS = 4

export class NotificationsModel {
  readonly #port: NotificationsPort
  #state: NotificationsState = { phase: 'loading', notifications: [], unread: 0, enabled: true, error: '' }
  #listeners = new Set<() => void>()
  #unsubscribe: (() => void) | null = null
  #timers = new Set<ReturnType<typeof setTimeout>>()

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
    return this.#state.notifications.slice(0, MAX_VISIBLE_TOASTS).filter((notification) => !this.#expired.has(notification.id))
  }

  /** Whether the toast is still showing. The model drives this so the stack
   * never duplicates a notification the timer already retired. */
  #expired = new Set<string>()

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

  async clear(): Promise<void> {
    try {
      await this.#port.clear()
      this.#expired.clear()
      this.#set({ ...this.#state, notifications: [], unread: 0 })
    } catch {
      this.#set({ ...this.#state, error: 'The feed could not be cleared' })
    }
  }

  #accept(notification: Notification): void {
    if (!notification?.id) return
    if (this.#state.notifications.some((existing) => existing.id === notification.id)) return
    const notifications = [notification, ...this.#state.notifications].slice(0, 200)
    this.#set({ ...this.#state, phase: 'ready', notifications, error: '' })
    if (this.#state.enabled) {
      this.#set({ ...this.#state, unread: this.#state.unread + 1 })
      // The toast retires itself; the notification itself stays in the feed.
      const timer = setTimeout(() => {
        this.#timers.delete(timer)
        this.#expired.add(notification.id)
        this.#notify()
      }, TOAST_TTL_MS)
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
