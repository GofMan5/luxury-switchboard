// @vitest-environment jsdom

import { describe, expect, it } from 'vitest'
import type { ActivityRequest, ActivitySummary } from '../domain/activity'
import type { ActivityPort } from './activity-port'
import { ActivityModel } from './activity-model'

const summary: ActivitySummary = { requests: 0, active: 0, queued: 0, successRate: 0, p95Ms: 0, rpm: 0 }
const request = (id: string, updatedAt: string): ActivityRequest => ({ id, startedAt: updatedAt, updatedAt, state: 'active', model: 'model', providerId: 'provider', providerName: 'Provider', method: 'POST', path: '/v1/responses', queueMs: 0, latencyMs: 0, retries: 0, bytesIn: 0, bytesOut: 0, inputTokens: 0, outputTokens: 0, cachedTokens: 0, reasoningTokens: 0, totalTokens: 0, contextTokens: 0, generationMs: 0, tokensPerSecond: 0 })

class FakeActivityPort implements ActivityPort {
  listener: ((request: ActivityRequest) => void) | null = null
  listPromise: Promise<readonly ActivityRequest[]> = Promise.resolve([])
  async list() { return this.listPromise }
  async summary() { return summary }
  subscribe(listener: (request: ActivityRequest) => void) { this.listener = listener; return () => { this.listener = null } }
}

describe('ActivityModel', () => {
  it('keeps events received while the initial history request is in flight', async () => {
    const port = new FakeActivityPort()
    let resolveList!: (requests: readonly ActivityRequest[]) => void
    port.listPromise = new Promise((resolve) => { resolveList = resolve })
    const model = new ActivityModel(port)
    const connecting = model.connect()
    const live = request('same', '2026-01-01T00:00:02Z')
    port.listener?.(live)
    resolveList([request('same', '2026-01-01T00:00:01Z'), request('history', '2026-01-01T00:00:00Z')])
    await connecting
    expect(model.snapshot().requests).toEqual([live, expect.objectContaining({ id: 'history' })])
    model.dispose()
  })
})
