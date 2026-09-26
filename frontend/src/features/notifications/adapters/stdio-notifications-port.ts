import type { ControlPlaneSession } from '../../../platform/stdio/session'

/** One thing the operator should know happened, already worded by its source. */
export interface Notification {
  readonly id: string
  readonly kind: string
  readonly severity: 'info' | 'success' | 'warning' | 'danger'
  readonly title: string
  readonly body: string
  readonly at: string
}

export interface NotificationsPort {
  list(signal?: AbortSignal): Promise<readonly Notification[]>
  clear(): Promise<void>
  subscribe(listener: (notification: Notification) => void): () => void
}

export class StdioNotificationsPort implements NotificationsPort {
  readonly #session: ControlPlaneSession

  constructor(session: ControlPlaneSession) {
    this.#session = session
  }

  async list(signal?: AbortSignal): Promise<readonly Notification[]> {
    const result = await this.#session.call<{ notifications?: readonly Notification[] }>(
      'notifications.list',
      undefined,
      signal,
    )
    return result.notifications ?? []
  }

  async clear(): Promise<void> {
    await this.#session.call('notifications.clear', undefined)
  }

  subscribe(listener: (notification: Notification) => void): () => void {
    return this.#session.subscribe<Notification>('notifications.raised', (event) => {
      if (event.payload) listener(event.payload)
    })
  }
}
