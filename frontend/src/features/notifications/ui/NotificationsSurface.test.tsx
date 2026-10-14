// @vitest-environment jsdom

import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ServicesContext, type AppServices } from '../../../app/services'
import type { Notification, NotificationsPort } from '../adapters/stdio-notifications-port'
import { NotificationsModel } from '../application/notifications-model'
import { NotificationsSurface } from './NotificationsSurface'

afterEach(() => { cleanup(); vi.useRealTimers(); delete document.documentElement.dataset.animations })

class Port implements NotificationsPort {
  listener: ((notification: Notification) => void) | undefined
  list = async () => []
  clear = async () => {}
  subscribe(listener: (notification: Notification) => void) { this.listener = listener; return () => { this.listener = undefined } }
}

const notification = (id: string): Notification => ({ id, kind: 'fixture', severity: 'warning', title: `Notice ${id}`, body: 'Safe fixture', at: '' })

describe('NotificationsSurface expiry', () => {
  it.each(['on', 'off'])('retires the DOM toast with app animations %s, without removing its feed entry', async (animations) => {
    vi.useFakeTimers()
    document.documentElement.dataset.animations = animations
    const port = new Port()
    const model = new NotificationsModel(port)
    await model.connect()
    render(<ServicesContext.Provider value={{ notifications: model } as AppServices}><NotificationsSurface /></ServicesContext.Provider>)
    act(() => port.listener?.(notification('one')))
    const beforeExpiry = model.snapshot()
    expect(screen.getByRole('region', { name: 'Notifications' }).textContent).toContain('Notice one')
    act(() => vi.advanceTimersByTime(5_299))
    expect(screen.getByRole('region', { name: 'Notifications' })).toBeTruthy()
    act(() => vi.advanceTimersByTime(2))
    expect(model.snapshot()).not.toBe(beforeExpiry)
    expect(screen.queryByRole('region', { name: 'Notifications' })).toBeNull()
    expect(model.snapshot().notifications).toEqual([notification('one')])
    fireEvent.click(screen.getByRole('button', { name: /Notifications/ }))
    expect(within(screen.getByRole('dialog')).getByText('Notice one')).toBeTruthy()
    model.dispose()
  })

  it('bounds the stack to four and expires each toast on its original deadline', async () => {
    vi.useFakeTimers()
    const port = new Port()
    const model = new NotificationsModel(port)
    await model.connect()
    render(<ServicesContext.Provider value={{ notifications: model } as AppServices}><NotificationsSurface /></ServicesContext.Provider>)
    act(() => { for (const id of ['one', 'two', 'three', 'four', 'five']) port.listener?.(notification(id)) })
    expect(within(screen.getByRole('region')).queryByText('Notice one')).toBeNull()
    expect(within(screen.getByRole('region')).getAllByText(/Notice/)).toHaveLength(4)
    act(() => vi.advanceTimersByTime(1_000))
    act(() => port.listener?.(notification('six')))
    act(() => vi.advanceTimersByTime(4_301))
    expect(within(screen.getByRole('region')).getAllByText(/Notice/).map((item) => item.textContent)).toEqual(['Notice six'])
    act(() => vi.advanceTimersByTime(1_000))
    expect(screen.queryByRole('region')).toBeNull()
    expect(model.snapshot().notifications).toHaveLength(6)
    model.dispose()
  })
})
