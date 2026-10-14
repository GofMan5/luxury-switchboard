import { afterEach, describe, expect, it, vi } from 'vitest'
import type { AddApiKey, ApiKey, UpdateApiKey } from '../domain/api-key'
import { ApiKeysModel } from './api-keys-model'
import type { ApiKeysListResult, ApiKeysPort, PoolCheckReport } from './api-keys-port'
import { ControlPlaneError } from '../../../shared/contracts/protocol'

const listed = (keys: readonly ApiKey[], available = keys.length): ApiKeysListResult => ({ keys, available })
const report: PoolCheckReport = { total: 0, checked: 0, skipped: 0, accepted: 0, answered: 0, rejected: 0, inconclusive: 0, reachable: false }
const key: ApiKey = { id: 'key-1', providerId: 'echo', label: 'Primary', priority: 0, rpm: 30, pinned: false, proxyConfigured: false, cooldownMs: 0, blockedModels: 0, retries429: 0, startsInWindow: 0, authStreak: 0, lastOutcome: '' }

class Port implements ApiKeysPort {
  listener: ((providerId: string) => void) | undefined
  finishAdd: ((value: ApiKey) => void) | undefined
  list: ApiKeysPort['list'] = async () => listed([])
  add = async (_value: AddApiKey) => new Promise<ApiKey>((resolve) => { this.finishAdd = resolve; this.listener?.('echo') })
  addMany: ApiKeysPort['addMany'] = async () => ({ added: 0, duplicate: [], rejected: [] })
  update = async (_value: UpdateApiKey) => key
  remove: ApiKeysPort['remove'] = async () => undefined
  move = async () => undefined
  reset: ApiKeysPort['reset'] = async () => undefined
  checkPool: ApiKeysPort['checkPool'] = async () => report
  subscribe = (listener: (providerId: string) => void) => { this.listener = listener; return () => undefined }
}

afterEach(() => vi.useRealTimers())

describe('ApiKeysModel', () => {
  it('keeps the full pool count and never counts fewer keys than it displays', async () => {
    const port = new Port()
    port.list = async () => listed([key], 500)
    const model = new ApiKeysModel(port)
    await model.load('echo')
    expect(model.snapshot()).toMatchObject({ keys: [key], available: 500 })
    port.list = async () => listed([key], 0)
    await model.load('echo')
    expect(model.snapshot()).toMatchObject({ keys: [key], available: 1 })
    port.list = async () => { throw new ControlPlaneError('key_pool_too_large', 'The key pool is too large to display') }
    await model.load('echo')
    expect(model.snapshot().error).toBe('The key pool is too large to display')
    model.dispose()
  })

  it('refreshes live metrics without events only while subscribed, with no overlap or loading flash', async () => {
    vi.useFakeTimers()
    const port = new Port()
    const list = vi.fn<ApiKeysPort['list']>().mockResolvedValue(listed([key]))
    port.list = list
    const model = new ApiKeysModel(port)
    await model.load('echo')
    await vi.advanceTimersByTimeAsync(10_000)
    expect(list).toHaveBeenCalledTimes(1)
    const unsubscribe = model.subscribe(() => undefined)
    let finish!: (result: ApiKeysListResult) => void
    list.mockImplementationOnce(async () => new Promise((resolve) => { finish = resolve }))
    await vi.advanceTimersByTimeAsync(2_000)
    expect(list).toHaveBeenCalledTimes(2)
    expect(model.snapshot().phase).toBe('ready')
    await vi.advanceTimersByTimeAsync(10_000)
    expect(list).toHaveBeenCalledTimes(2)
    finish(listed([{ ...key, startsInWindow: 7, cooldownMs: 1_000, retries429: 2 }]))
    await vi.advanceTimersByTimeAsync(0)
    expect(model.snapshot().keys[0]).toMatchObject({ startsInWindow: 7, cooldownMs: 1_000, retries429: 2 })
    unsubscribe()
    await vi.advanceTimersByTimeAsync(10_000)
    expect(list).toHaveBeenCalledTimes(2)
    expect(vi.getTimerCount()).toBe(0)
    model.dispose()
  })

  it('clears a finished mutation even when its terminal reload is overtaken by a change event', async () => {
    const port = new Port()
    const model = new ApiKeysModel(port)
    await model.load('echo')
    let finishReload!: (result: ApiKeysListResult) => void
    port.list = vi.fn<ApiKeysPort['list']>()
      .mockImplementationOnce(async () => new Promise((resolve) => { finishReload = resolve }))
      .mockResolvedValue(listed([key]))
    const mutation = model.reset(key.id)
    await Promise.resolve()
    await Promise.resolve()
    port.listener?.('echo')
    finishReload(listed([]))
    expect(await mutation).toBe(true)
    expect(model.snapshot()).toMatchObject({ keys: [key], pendingId: '' })
    model.dispose()
  })

  it.each(['switch', 'dispose'] as const)('does not apply a late mutation failure after %s', async (lifecycle) => {
    const port = new Port()
    const model = new ApiKeysModel(port)
    await model.load('echo')
    let reject!: (error: Error) => void
    let signal: AbortSignal | undefined
    port.reset = async (_providerId, _keyId, abortSignal) => new Promise((_resolve, no) => { reject = no; signal = abortSignal })
    const mutation = model.reset(key.id)
    if (lifecycle === 'switch') {
      port.list = async () => { throw new Error('new provider unavailable') }
      await model.load('other')
    } else model.dispose()
    const before = model.snapshot()
    expect(signal?.aborted).toBe(true)
    reject(new Error('old mutation failed'))
    expect(await mutation).toBe(false)
    expect(model.snapshot()).toBe(before)
    model.dispose()
  })

  it.each(['switch', 'dispose'] as const)('cancels the actual pool check on %s and ignores its cancellation error', async (lifecycle) => {
    const port = new Port()
    const model = new ApiKeysModel(port)
    await model.load('echo')
    let signal: AbortSignal | undefined
    let reject!: (error: Error) => void
    port.checkPool = async (_providerId, abortSignal) => new Promise((_yes, no) => { signal = abortSignal; reject = no })
    const checking = model.checkPool()
    if (lifecycle === 'switch') await model.load('other')
    else model.dispose()
    const before = model.snapshot()
    const aborted = signal?.aborted
    reject(new ControlPlaneError('canceled', 'The pool check was canceled'))
    await checking
    expect(aborted).toBe(true)
    expect(model.snapshot()).toBe(before)
    model.dispose()
  })

  it('discards a pool check when switching providers before its check completes', async () => {
    const port = new Port()
    let finish!: (report: PoolCheckReport) => void
    port.checkPool = vi.fn<ApiKeysPort['checkPool']>(async () => new Promise((resolve) => { finish = resolve }))
    const list = vi.fn<ApiKeysPort['list']>().mockImplementation(async (providerId) => listed([{ ...key, providerId }]))
    port.list = list
    const model = new ApiKeysModel(port)
    await model.load('echo')
    const checking = model.checkPool()
    await model.load('other')
    expect(model.snapshot().checkingPool).toBe(false)
    finish({ ...report, total: 1, checked: 1, answered: 1, rejected: 1, reachable: true })
    await checking
    expect(port.checkPool).toHaveBeenCalledWith('echo', expect.any(AbortSignal))
    expect(list.mock.calls.map(([id]) => id)).toEqual(['echo', 'other'])
    expect(model.snapshot()).toMatchObject({ providerId: 'other', poolReport: null, checkingPool: false, error: '' })
    model.dispose()
  })

  it.each(['success', 'failure'] as const)('ignores a late %s from the checked provider list, even after switching back', async (outcome) => {
    const port = new Port()
    const model = new ApiKeysModel(port)
    await model.load('echo')
    let resolve!: (result: ApiKeysListResult) => void
    let reject!: (error: Error) => void
    port.list = async () => new Promise((yes, no) => { resolve = yes; reject = no })
    const checking = model.checkPool()
    await Promise.resolve()
    port.list = async (providerId) => listed([{ ...key, id: 'fresh', providerId }])
    await model.load('other')
    await model.load('echo')
    if (outcome === 'success') resolve(listed([{ ...key, id: 'stale' }]))
    else reject(new Error('late failure'))
    await checking
    expect(model.snapshot()).toMatchObject({ providerId: 'echo', keys: [{ id: 'fresh' }], poolReport: null, checkingPool: false, error: '' })
    model.dispose()
  })

  it('does not resurrect a removed key when the pool followup list finishes after a newer refresh', async () => {
    const port = new Port()
    const model = new ApiKeysModel(port)
    await model.load('echo')
    let finish!: (result: ApiKeysListResult) => void
    port.list = vi.fn<ApiKeysPort['list']>()
      .mockImplementationOnce(async () => new Promise((resolve) => { finish = resolve }))
      .mockResolvedValue(listed([]))
    const checking = model.checkPool()
    await Promise.resolve()
    await model.load('echo')
    finish(listed([key]))
    await checking
    expect(model.snapshot()).toMatchObject({ keys: [], checkingPool: false, poolReport: { checked: 0 } })
    model.dispose()
  })

  it('keeps a pool check alive through same-provider change events', async () => {
    const port = new Port()
    port.list = async () => listed([key])
    port.checkPool = async () => { port.listener?.('echo'); return { ...report, total: 1, checked: 1, accepted: 1, answered: 1, reachable: true } }
    const model = new ApiKeysModel(port)
    await model.load('echo')
    await model.checkPool()
    expect(model.snapshot()).toMatchObject({ keys: [key], checkingPool: false, poolReport: { checked: 1, rejected: 0 } })
    model.dispose()
  })

  it('removes rejected user keys without attempting pinned or healthy keys', async () => {
    const port = new Port()
    const keys = [{ ...key, id: 'pinned', pinned: true, authStreak: 3 }, { ...key, id: 'rejected', authStreak: 3 }, { ...key, id: 'also-rejected', authStreak: 4 }, key]
    port.list = async () => listed(keys)
    const removed: string[] = []
    port.remove = async (_providerId: string, id: string) => {
      if (id === 'pinned') throw new Error('managed key')
      removed.push(id)
    }
    const model = new ApiKeysModel(port)
    await model.load('echo')
    await model.removeRejected()
    expect(removed).toEqual(['rejected', 'also-rejected'])
    expect(model.snapshot().error).toBe('')
    model.dispose()
  })

  it('keeps a mutation pending while its change event refreshes the list', async () => {
    const port = new Port()
    const model = new ApiKeysModel(port)
    await model.load('echo')
    const mutation = model.add({ providerId: 'echo', label: 'Primary', secret: 'secret', rpm: 30, proxyUrl: '' })
    await Promise.resolve()
    await Promise.resolve()
    expect(model.snapshot().pendingId).toBe('new')
    port.finishAdd?.(key)
    expect(await mutation).toBe(true)
    expect(model.snapshot().pendingId).toBe('')
  })

  it('reports what a bulk import added and skipped, and never sends an empty batch', async () => {
    const port = new Port()
    let batches = 0
    port.addMany = async (value) => { batches += 1; return { added: value.entries.length - 1, duplicate: [1], rejected: [] } }
    const model = new ApiKeysModel(port)
    await model.load('echo')
    const report = await model.importKeys({
      providerId: 'echo', rpm: 30, proxyUrl: '',
      entries: [{ label: 'One', secret: 'one' }, { label: 'Again', secret: 'one' }],
    })
    expect(report).toEqual({ added: 1, duplicate: [1], rejected: [] })
    expect(model.snapshot().pendingId).toBe('')
    expect(await model.importKeys({ providerId: 'echo', rpm: 30, proxyUrl: '', entries: [] })).toBeNull()
    expect(batches).toBe(1)
  })

  it('does not show keys from the previous provider while switching', async () => {
    let finishSwitch!: (value: ApiKeysListResult) => void
    const port = new Port()
    port.list = async (providerId: string) => providerId === 'echo' ? listed([key]) : await new Promise<ApiKeysListResult>((resolve) => { finishSwitch = resolve })
    const model = new ApiKeysModel(port)
    await model.load('echo')
    const switching = model.load('other')
    expect(model.snapshot()).toMatchObject({ providerId: 'other', phase: 'loading', keys: [], available: 0 })
    finishSwitch(listed([]))
    await switching
  })
})
