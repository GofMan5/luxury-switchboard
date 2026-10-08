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
        queueMicrotask(() => mocks.listeners.get('sidecar-frame')?.({ payload: JSON.stringify({ v: 1, id: frame.id, type: 'result', ok: true, payload: { appVersion: '9.9.9' } }) }))
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

  it('takes its version from the handshake and refuses anything else', async () => {
    const session = new TauriSidecarSession()
    expect(session.appVersion).toBe('')
    await session.start()
    expect(session.appVersion).toBe('9.9.9')
    await session.stop()

    mocks.invoke.mockImplementation(async (command: string, args?: { frame?: string }) => {
      if (command !== 'sidecar_write' || !args?.frame) return undefined
      const frame = JSON.parse(args.frame) as { id: string; method: string }
      if (frame.method === 'system.handshake') {
        queueMicrotask(() => mocks.listeners.get('sidecar-frame')?.({ payload: JSON.stringify({ v: 1, id: frame.id, type: 'result', ok: true, payload: { appVersion: '<script>1.0.0' } }) }))
      }
      return undefined
    })
    const hostile = new TauriSidecarSession()
    await hostile.start()
    expect(hostile.appVersion).toBe('')
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

  it('does not leave a model-test command pending indefinitely', async () => {
    const session = new TauriSidecarSession()
    await session.start()
    vi.useFakeTimers()
    const call = session.call('models.test')
    const rejection = expect(call).rejects.toMatchObject({ code: 'timeout' })
    await vi.advanceTimersByTimeAsync(3 * 60_000)
    await rejection
    const command = mocks.writes.find((frame) => frame.method === 'models.test')
    expect(mocks.writes).toContainEqual(expect.objectContaining({ method: 'system.cancel', payload: { id: command?.id } }))
    await session.stop()
    vi.useRealTimers()
  })

  it('gives the codex quota probe the full backend chain, not the default call guard', async () => {
    const session = new TauriSidecarSession()
    await session.start()
    vi.useFakeTimers()
    const call = session.call('codex.quota')
    // The backend worst case is token refresh + 401 retry + two 20s probes.
    // The default 30s guard would fire here and blame the relay.
    await vi.advanceTimersByTimeAsync(30_000)
    let timedOut = false
    void call.then(
      () => {},
      () => { timedOut = true },
    )
    expect(timedOut).toBe(false)
    const rejection = expect(call).rejects.toMatchObject({ code: 'timeout' })
    await vi.advanceTimersByTimeAsync(60_000)
    await rejection
    const command = mocks.writes.find((frame) => frame.method === 'codex.quota')
    expect(mocks.writes).toContainEqual(expect.objectContaining({ method: 'system.cancel', payload: { id: command?.id } }))
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

  it('rejects an in-flight command as soon as its signal is aborted', async () => {
    const session = new TauriSidecarSession()
    await session.start()
    const controller = new AbortController()
    const call = session.call('models.test', {}, controller.signal)
    await vi.waitFor(() => expect(mocks.writes.some((frame) => frame.method === 'models.test')).toBe(true))
    const command = mocks.writes.find((frame) => frame.method === 'models.test')
    controller.abort()
    await expect(call).rejects.toMatchObject({ name: 'AbortError' })
    expect(mocks.writes).toContainEqual(expect.objectContaining({ method: 'system.cancel', payload: { id: command?.id } }))
    await session.stop()
  })
})
