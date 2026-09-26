// @vitest-environment jsdom

import { describe, expect, it } from 'vitest'
import type { Notification, NotificationsPort } from '../adapters/stdio-notifications-port'
import { NotificationsModel } from './notifications-model'

const notification = (id: string, title: string): Notification => ({
  id, kind: 'provider_failover', severity: 'warning', title, body: 'body', at: '2026-09-27T12:00:00Z',
})

class FakeNotificationsPort implements NotificationsPort {
  listener: ((notification: Notification) => void) | null = null
  listPromise: Promise<readonly Notification[]> = Promise.resolve([])
  cleared = 0
  async list() { return this.listPromise }
  async clear() { this.cleared++ }
  subscribe(listener: (notification: Notification) => void) { this.listener = listener; return () => { this.listener = null } }
}

describe('NotificationsModel', () => {
  it('feeds live notifications and counts the unread ones', async () => {
    const port = new FakeNotificationsPort()
    const model = new NotificationsModel(port)
    await model.connect()
    port.listener?.(notification('n1', 'glm failed over to backup'))
    const state = model.snapshot()
    expect(state.notifications).toHaveLength(1)
    expect(state.unread).toBe(1)
    model.markRead()
    expect(model.snapshot().unread).toBe(0)
    model.dispose()
  })

  it('drops duplicate ids and records the feed without toasts when disabled', async () => {
    const port = new FakeNotificationsPort()
    const model = new NotificationsModel(port)
    await model.connect()
    model.setEnabled(false)
    port.listener?.(notification('n1', 'same'))
    port.listener?.(notification('n1', 'same'))
    const state = model.snapshot()
    expect(state.notifications).toHaveLength(1)
    expect(state.unread).toBe(0)
    expect(model.toasts()).toHaveLength(0)
    model.dispose()
  })

  it('clears through the port', async () => {
    const port = new FakeNotificationsPort()
    const model = new NotificationsModel(port)
    await model.connect()
    port.listener?.(notification('n1', 'x'))
    await model.clear()
    expect(port.cleared).toBe(1)
    expect(model.snapshot().notifications).toHaveLength(0)
    model.dispose()
  })
})
