// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

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
  afterEach(() => vi.useRealTimers())

  it.each(['timeout', 'abort', 'disconnect'] as const)('settles %s while the native send remains blocked', async (outcome) => {
    const session = new TauriSidecarSession()
    await session.start()
    vi.useFakeTimers()
    const invoke = mocks.invoke.getMockImplementation()!
    let release!: () => void
    const blocked = new Promise<void>((resolve) => { release = resolve })
    mocks.invoke.mockImplementation(async (command: string, args?: { frame?: string }) => {
      if (command === 'sidecar_write' && args?.frame && JSON.parse(args.frame).method === 'models.test') {
        mocks.writes.push(JSON.parse(args.frame))
        await blocked
        return
      }
      return invoke(command, args)
    })
    const controller = new AbortController()
    let error: unknown
    let settled = false
    const call = session.call('models.test', {}, controller.signal).then(
      () => { settled = true },
      (reason) => { error = reason; settled = true },
    )
    await vi.advanceTimersByTimeAsync(0)
    expect(mocks.writes.some((frame) => frame.method === 'models.test')).toBe(true)
    if (outcome === 'timeout') await vi.advanceTimersByTimeAsync(3 * 60_000)
    else if (outcome === 'abort') controller.abort()
    else mocks.listeners.get('sidecar-lifecycle')?.({ payload: { state: 'stopped' } })
    await vi.advanceTimersByTimeAsync(0)
    // Release only after observing the public result: no hung test on the old code.
    const settledBeforeSend = settled
    release()
    await call
    await session.stop()
    expect(settledBeforeSend).toBe(true)
    expect(error).toMatchObject(outcome === 'abort' ? { name: 'AbortError' } : { code: outcome === 'timeout' ? 'timeout' : 'disconnected' })
  })

  it('hard-stops after the bounded exit wait even when shutdown send is blocked', async () => {
    const session = new TauriSidecarSession()
    await session.start()
    vi.useFakeTimers()
    const invoke = mocks.invoke.getMockImplementation()!
    let release!: () => void
    const blocked = new Promise<void>((resolve) => { release = resolve })
    mocks.invoke.mockImplementation(async (command: string, args?: { frame?: string }) => {
      if (command === 'sidecar_write' && args?.frame && JSON.parse(args.frame).method === 'system.shutdown') {
        await blocked
        return
      }
      return invoke(command, args)
    })
    let settled = false
    const stopping = session.stop().then(() => { settled = true })
    await vi.advanceTimersByTimeAsync(10_000)
    const stoppedBeforeSend = settled
    release()
    await stopping
    expect(stoppedBeforeSend).toBe(true)
    expect(mocks.invoke).toHaveBeenCalledWith('sidecar_stop')
    expect(mocks.listeners.size).toBe(0)
  })

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

  it('lets pool checks reach their two-minute deadline and cancels only after 130 seconds', async () => {
    const session = new TauriSidecarSession()
    await session.start()
    vi.useFakeTimers()
    let settled = false
    const call = session.call('keys.check', { providerId: 'echo' }).then(
      () => { settled = true; return undefined },
      (error: unknown) => { settled = true; return error },
    )
    await vi.advanceTimersByTimeAsync(120_000)
    const settledAtBackendDeadline = settled
    await vi.advanceTimersByTimeAsync(9_999)
    const settledBeforeGuard = settled
    await vi.advanceTimersByTimeAsync(1)
    const error = await call
    await session.stop()
    expect(settledAtBackendDeadline).toBe(false)
    expect(settledBeforeGuard).toBe(false)
    expect(error).toMatchObject({ code: 'timeout' })
    const command = mocks.writes.find((frame) => frame.method === 'keys.check')
    expect(mocks.writes).toContainEqual(expect.objectContaining({ method: 'system.cancel', payload: { id: command?.id } }))
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

  it('gives the codex fleet probes the full backend chain, not the default call guard', async () => {
    // The fleet quota and the wake run per-account chains in parallel under
    // one call: they carry the single probe's budget, not 30s times N.
    for (const method of ['codex.quota.all', 'codex.wake']) {
      const session = new TauriSidecarSession()
      await session.start()
      vi.useFakeTimers()
      const call = session.call(method)
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
      const command = mocks.writes.find((frame) => frame.method === method)
      expect(mocks.writes).toContainEqual(expect.objectContaining({ method: 'system.cancel', payload: { id: command?.id } }))
      await session.stop()
      vi.useRealTimers()
    }
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

  it('guards the complete import payload in UTF-8 bytes before native send without splitting', async () => {
    const session = new TauriSidecarSession()
    await session.start()
    vi.useFakeTimers()
    const value = { providerId: 'echo', rpm: 30, proxyUrl: 'https://proxy.example/' + 'p'.repeat(8_000), entries: [{ label: 'Example', secret: '🔑'.repeat(63_000) }] }
    expect(JSON.stringify(value.entries).length).toBeLessThan(240_000)
    expect(new TextEncoder().encode(JSON.stringify(value)).byteLength).toBeGreaterThan(258_048)
    let error: unknown
    const call = session.call('keys.addMany', value).catch((reason: unknown) => { error = reason })
    await vi.advanceTimersByTimeAsync(30_000)
    await call
    const writes = mocks.writes.filter((frame) => frame.method === 'keys.addMany' || frame.method === 'system.cancel')
    const timers = vi.getTimerCount()
    await session.stop()
    expect(error).toMatchObject({ code: 'command_too_large', message: expect.stringContaining('smaller batches') })
    expect(writes).toEqual([])
    expect(timers).toBe(0)
  })

  it('accepts the payload byte boundary but rejects one extra byte and an oversized envelope', async () => {
    const session = new TauriSidecarSession()
    await session.start()
    const value = { providerId: 'echo', rpm: 30, proxyUrl: '', entries: [{ label: 'Example', secret: '' }] }
    value.entries[0].secret = 's'.repeat(258_048 - new TextEncoder().encode(JSON.stringify(value)).byteLength)
    const controller = new AbortController()
    const accepted = session.call('keys.addMany', value, controller.signal).catch((error: unknown) => error)
    await vi.waitFor(() => expect(mocks.writes.some((frame) => frame.method === 'keys.addMany')).toBe(true))
    controller.abort()
    expect(await accepted).toMatchObject({ name: 'AbortError' })
    await expect(session.call('keys.addMany', { ...value, proxyUrl: 'x' })).rejects.toMatchObject({ code: 'command_too_large' })
    await expect(session.call('m'.repeat(5_000), value)).rejects.toMatchObject({ code: 'command_too_large' })
    expect(mocks.writes.filter((frame) => frame.method === 'keys.addMany')).toHaveLength(1)
    await session.stop()
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
