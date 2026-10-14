// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ActivityRequest, ActivitySummary } from '../domain/activity'
import type { ActivityListResult, ActivityPort } from './activity-port'
import { ActivityModel } from './activity-model'

const summary: ActivitySummary = { requests: 0, active: 0, queued: 0, successRate: 0, p95Ms: 0, rpm: 0 }
const request = (id: string, updatedAt: string): ActivityRequest => ({ id, startedAt: updatedAt, updatedAt, state: 'active', model: 'model', providerId: 'provider', providerName: 'Provider', method: 'POST', path: '/v1/responses', queueMs: 0, latencyMs: 0, retries: 0, bytesIn: 0, bytesOut: 0, inputTokens: 0, outputTokens: 0, cachedTokens: 0, reasoningTokens: 0, totalTokens: 0, contextTokens: 0, generationMs: 0, tokensPerSecond: 0 })

class FakeActivityPort implements ActivityPort {
  listener: ((request: ActivityRequest) => void) | null = null
  listPromise: Promise<ActivityListResult> = Promise.resolve({ requests: [], available: 0 })
  async list() { return this.listPromise }
  async summary() { return summary }
  subscribe(listener: (request: ActivityRequest) => void) { this.listener = listener; return () => { this.listener = null } }
}

afterEach(() => vi.useRealTimers())

describe('ActivityModel', () => {
  it('never lets an older live event replace a terminal row or undercounts visible rows', async () => {
    const port = new FakeActivityPort()
    const model = new ActivityModel(port)
    await model.connect()
    const terminal = { ...request('same', '2026-01-01T00:00:02Z'), state: 'completed' as const }
    port.listener?.(terminal)
    expect(model.snapshot().available).toBe(1)
    port.listener?.(request('same', '2026-01-01T00:00:01Z'))
    expect(model.snapshot().requests).toEqual([terminal])
    model.dispose()
  })

  it.each(['success', 'failure'] as const)('ignores connect %s after disposal without starting timers', async (outcome) => {
    vi.useFakeTimers()
    const port = new FakeActivityPort()
    let resolve!: (result: ActivityListResult) => void
    let reject!: (error: Error) => void
    port.listPromise = new Promise((yes, no) => { resolve = yes; reject = no })
    const model = new ActivityModel(port)
    const connecting = model.connect()
    model.dispose()
    const before = model.snapshot()
    if (outcome === 'success') resolve({ requests: [request('late', 'now')], available: 1 })
    else reject(new Error('late failure'))
    await connecting
    const after = model.snapshot()
    const timers = vi.getTimerCount()
    model.dispose()
    expect(after).toBe(before)
    expect(timers).toBe(0)
  })

  it('ignores an old summary after dispose and reconnect and allows fresh polling', async () => {
    vi.useFakeTimers()
    const port = new FakeActivityPort()
    const model = new ActivityModel(port)
    await model.connect()
    let finish!: (value: ActivitySummary) => void
    const summaryCall = vi.spyOn(port, 'summary')
      .mockImplementationOnce(async () => new Promise((resolve) => { finish = resolve }))
      .mockResolvedValue({ ...summary, requests: 2 })
    await vi.advanceTimersByTimeAsync(1_000)
    model.dispose()
    await model.connect()
    finish({ ...summary, requests: 99 })
    await vi.advanceTimersByTimeAsync(0)
    expect(model.snapshot().summary.requests).toBe(2)
    await vi.advanceTimersByTimeAsync(1_000)
    expect(summaryCall).toHaveBeenCalledTimes(3)
    model.dispose()
  })

  it('keeps an already received terminal row when a refresh returns an older snapshot', async () => {
    const port = new FakeActivityPort()
    const model = new ActivityModel(port)
    await model.connect()
    const terminal = { ...request('same', '2026-01-01T00:00:02Z'), state: 'completed' as const }
    port.listener?.(terminal)
    port.listPromise = Promise.resolve({ requests: [request('same', '2026-01-01T00:00:01Z')], available: 1 })
    await model.connect()
    expect(model.snapshot().requests).toEqual([terminal])
    model.dispose()
  })

  it('accepts a newer history terminal over an older event received during connect', async () => {
    const port = new FakeActivityPort()
    let finish!: (result: ActivityListResult) => void
    port.listPromise = new Promise((resolve) => { finish = resolve })
    const model = new ActivityModel(port)
    const connecting = model.connect()
    port.listener?.(request('same', '2026-01-01T00:00:01Z'))
    const terminal = { ...request('same', '2026-01-01T00:00:02Z'), state: 'completed' as const }
    finish({ requests: [terminal], available: 1 })
    await connecting
    expect(model.snapshot().requests).toEqual([terminal])
    model.dispose()
  })

  it('keeps events received while the initial history request is in flight', async () => {
    const port = new FakeActivityPort()
    let resolveList!: (result: ActivityListResult) => void
    port.listPromise = new Promise((resolve) => { resolveList = resolve })
    const model = new ActivityModel(port)
    const connecting = model.connect()
    const live = request('same', '2026-01-01T00:00:02Z')
    port.listener?.(live)
    resolveList({ requests: [request('same', '2026-01-01T00:00:01Z'), request('history', '2026-01-01T00:00:00Z')], available: 2 })
    await connecting
    expect(model.snapshot().requests).toEqual([live, expect.objectContaining({ id: 'history' })])
    model.dispose()
  })

  it('resubscribes and restarts polling when it connects again after dispose', async () => {
    // dispose() must clear the saved unsubscribe and poll timer: both fields are
    // ??=-guarded in connect(), so a stale value would silently skip the new
    // subscription and leave the model deaf after a reconnect.
    const port = new FakeActivityPort()
    const model = new ActivityModel(port)
    await model.connect()
    expect(port.listener).not.toBeNull()
    model.dispose()
    expect(port.listener).toBeNull()
    await model.connect()
    expect(port.listener).not.toBeNull()
    model.dispose()
  })

  it('carries the available count so a truncated list reads as a short list', async () => {
    // The frame budget drops the oldest rows during a provider incident; the
    // footer must say "Newest N of M" instead of passing a short list off as
    // the whole buffer.
    const port = new FakeActivityPort()
    port.listPromise = Promise.resolve({ requests: [request('newest', '2026-01-01T00:00:05Z')], available: 41 })
    const model = new ActivityModel(port)
    await model.connect()
    expect(model.snapshot().available).toBe(41)
    model.dispose()
  })
})
