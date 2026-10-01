import { describe, expect, it, vi } from 'vitest'
import { StdioInsightsPort } from './stdio-insights-port'
import type { ControlPlaneSession } from '../../../platform/stdio/session'

function sessionWith(answer: Record<string, unknown>): ControlPlaneSession {
  return {
    start: vi.fn(async () => undefined),
    call: vi.fn(async () => answer) as ControlPlaneSession['call'],
    subscribe: vi.fn(() => () => undefined),
    stop: vi.fn(async () => undefined),
  }
}

describe('StdioInsightsPort.recent', () => {
  const rows = [{ id: 'r1', state: 'completed', model: 'm', providerId: 'p', status: 200, latencyMs: 1, totalTokens: 1, cachedTokens: 0, updatedAt: '2026-09-29T12:00:00Z', errorCode: '', errorDetail: '' }]

  it('carries the untruncated count the backend reports', async () => {
    const port = new StdioInsightsPort(sessionWith({ requests: rows, available: 250 }))
    const recent = await port.recent('24h')
    expect(recent.rows).toEqual(rows)
    expect(recent.available).toBe(250)
  })

  // The tolerant default must not silently re-collapse the honesty feature:
  // when the backend stops answering `available`, a bounded list would read as
  // the whole journal again. The default is pinned so the drift is a decision
  // made here, not a silent lie downstream.
  it('falls back to the row count only when the backend answered without one', async () => {
    const port = new StdioInsightsPort(sessionWith({ requests: rows }))
    const recent = await port.recent('24h')
    expect(recent.available).toBe(1)
  })
})
