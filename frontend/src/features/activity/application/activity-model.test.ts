// @vitest-environment jsdom

import { describe, expect, it } from 'vitest'
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

describe('ActivityModel', () => {
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
