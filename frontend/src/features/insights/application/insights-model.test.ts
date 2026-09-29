import { beforeEach, describe, expect, it, vi } from 'vitest'
import { InsightsModel } from './insights-model'
import type { InsightsPort } from './insights-port'
import type { InsightsReport, HistoryRequest, InsightsPeriod, ModelPrice } from '../domain/insights'

function emptyReport(period: InsightsPeriod): InsightsReport {
  return {
    period, generatedAt: '2026-09-29T12:00:00Z',
    overview: {
      volume: { requests: 0, completed: 0, failed: 0, cancelled: 0, retries: 0, inputTokens: 0, outputTokens: 0, cachedTokens: 0, reasoningTokens: 0, totalTokens: 0, generationMs: 0, cost: 0, isPriced: false },
      successRate: 0, p50Ms: 0, p95Ms: 0, tokensPerSecond: 0, pricedRequests: 0, topErrorCode: '',
    },
    providers: [], models: [], daily: [], errors: [], unpricedModels: [],
  }
}

function request(id: string): HistoryRequest {
  return { id, state: 'completed', model: 'm', providerId: 'p', status: 200, latencyMs: 10, totalTokens: 1, cachedTokens: 0, updatedAt: '2026-09-29T12:00:00Z', errorCode: '', errorDetail: '' }
}

class FakePort implements InsightsPort {
  report = vi.fn(async (period: InsightsPeriod): Promise<InsightsReport> => emptyReport(period))
  recent = vi.fn(async (period: InsightsPeriod): Promise<readonly HistoryRequest[]> => [request(`r-${period}`)])
  prices = vi.fn(async (): Promise<readonly ModelPrice[]> => [])
  setPrice = vi.fn(async (): Promise<readonly ModelPrice[]> => [])
  removePrice = vi.fn(async (): Promise<readonly ModelPrice[]> => [])
}

describe('InsightsModel', () => {
  let port: FakePort
  let model: InsightsModel
  beforeEach(() => {
    port = new FakePort()
    model = new InsightsModel(port)
  })

  it('loads the report, the recent rows and the prices as one snapshot', async () => {
    await model.load('24h')
    const state = model.snapshot()
    expect(state.phase).toBe('ready')
    expect(state.report?.period).toBe('24h')
    expect(state.recent[0]?.id).toBe('r-24h')
    expect(port.recent).toHaveBeenCalledWith('24h')
  })

  it('clears the stale rows while a new period loads, and keeps them on a plain refresh', async () => {
    await model.load('24h')
    let release: ((rows: readonly HistoryRequest[]) => void) | undefined
    const rows = new Promise<readonly HistoryRequest[]>((resolve) => { release = resolve })
    port.recent.mockImplementationOnce(async () => rows)
    const loading = model.load('48h')
    // The old period's rows must not sit under the new period's label while
    // its own answer is still in flight.
    expect(model.snapshot().recent).toEqual([])
    release?.([request('r-48h')])
    await loading
    expect(model.snapshot().recent[0]?.id).toBe('r-48h')
    await model.load('48h')
    expect(model.snapshot().recent[0]?.id).toBe('r-48h')
  })

  it('answers an error phase without inventing rows', async () => {
    port.report.mockRejectedValue(new Error('down'))
    await model.load('24h')
    const state = model.snapshot()
    expect(state.phase).toBe('error')
    expect(state.recent).toEqual([])
    expect(state.report).toBeNull()
  })

  it('closes the editor only on a confirmed save, and reloads the report after it', async () => {
    port.setPrice.mockResolvedValue([{ model: 'm', input: 1, cachedInput: 0.1, output: 2, reasoning: 0, updatedAt: '2026-09-29T12:00:00Z' }])
    await model.load('24h')
    const saved = await model.savePrice({ model: 'm', input: 1, cachedInput: 0.1, output: 2, reasoning: 0 })
    expect(saved).toBe(true)
    expect(port.report.mock.calls.length).toBeGreaterThan(1)
    port.setPrice.mockRejectedValue(new Error('refused'))
    const refused = await model.savePrice({ model: 'm', input: 1, cachedInput: 0.1, output: 2, reasoning: 0 })
    expect(refused).toBe(false)
    expect(model.snapshot().pricesPhase).toBe('error')
  })

  it('ignores a stale load that resolves after a newer one started', async () => {
    let releaseOld: ((value: InsightsReport) => void) | undefined
    port.report.mockImplementationOnce((period: InsightsPeriod) => new Promise<InsightsReport>((resolve) => {
      releaseOld = () => resolve(emptyReport(period))
    }))
    const first = model.load('24h')
    await model.load('48h')
    releaseOld?.(emptyReport('24h'))
    await first
    // The slower 24h answer arrived last but must not overwrite the 48h state.
    expect(model.snapshot().period).toBe('48h')
    expect(model.snapshot().report?.period).toBe('48h')
  })
})
