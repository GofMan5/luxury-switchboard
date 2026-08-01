// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  invoke: vi.fn(),
  listen: vi.fn(),
  listeners: new Map<string, (event: { payload: unknown }) => void>(),
  writes: [] as Array<{ id: string; method: string; payload?: { id?: string } }>,
}))

vi.mock('@tauri-apps/api/core', () => ({ invoke: mocks.invoke }))
vi.mock('@tauri-apps/api/event', () => ({ listen: mocks.listen }))

import { TauriSidecarSession } from './tauri-sidecar-session'

describe('TauriSidecarSession', () => {
  beforeEach(() => {
    mocks.listeners.clear()
    mocks.writes.length = 0
    mocks.listen.mockReset().mockImplementation(async (topic: string, listener: (event: { payload: unknown }) => void) => {
      mocks.listeners.set(topic, listener)
      return () => { mocks.listeners.delete(topic) }
    })
    mocks.invoke.mockReset().mockImplementation(async (command: string, args?: { frame?: string }) => {
      if (command !== 'sidecar_write' || !args?.frame) return undefined
      const frame = JSON.parse(args.frame) as { id: string; method: string; payload?: { id?: string } }
      mocks.writes.push(frame)
      if (frame.method === 'system.handshake') {
        queueMicrotask(() => mocks.listeners.get('sidecar-frame')?.({ payload: JSON.stringify({ v: 1, id: frame.id, type: 'result', ok: true, payload: {} }) }))
      }
      if (frame.method === 'system.shutdown') {
        queueMicrotask(() => {
          mocks.listeners.get('sidecar-frame')?.({ payload: JSON.stringify({ v: 1, id: frame.id, type: 'result', ok: true }) })
          queueMicrotask(() => mocks.listeners.get('sidecar-lifecycle')?.({ payload: { state: 'stopped' } }))
        })
      }
      return undefined
    })
  })

  it('allows tunnel startup time and cancels the backend command on timeout', async () => {
    const session = new TauriSidecarSession()
    await session.start()
    vi.useFakeTimers()
    const call = session.call('tunnel.start')
    const rejection = expect(call).rejects.toMatchObject({ code: 'timeout' })
    await vi.advanceTimersByTimeAsync(60_000)
    await rejection
    const start = mocks.writes.find((frame) => frame.method === 'tunnel.start')
    expect(mocks.writes).toContainEqual(expect.objectContaining({ method: 'system.cancel', payload: { id: start?.id } }))
    await session.stop()
    vi.useRealTimers()
  })

  it('reconnects after sidecar termination and notifies state models', async () => {
    const session = new TauriSidecarSession()
    await session.start()
    const reconnected = vi.fn()
    session.subscribe('system.reconnected', reconnected)
    vi.useFakeTimers()
    mocks.listeners.get('sidecar-lifecycle')?.({ payload: { state: 'stopped' } })
    await vi.advanceTimersByTimeAsync(1_000)
    expect(reconnected).toHaveBeenCalledOnce()
    expect(mocks.invoke.mock.calls.filter(([command]) => command === 'sidecar_start')).toHaveLength(2)
    await session.stop()
    vi.useRealTimers()
  })

  it('does not send a command that was cancelled before dispatch', async () => {
    const session = new TauriSidecarSession()
    await session.start()
    const controller = new AbortController()
    controller.abort()
    await expect(session.call('models.test', {}, controller.signal)).rejects.toMatchObject({ name: 'AbortError' })
    expect(mocks.writes.some((frame) => frame.method === 'models.test')).toBe(false)
    await session.stop()
  })
})
