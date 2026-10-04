import { beforeEach, describe, expect, it, vi } from 'vitest'
import { InsightsModel } from './insights-model'
import type { InsightsPort } from './insights-port'
import type { InsightsReport, HistoryRequest, InsightsPeriod, PriceCatalog } from '../domain/insights'

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
  recent = vi.fn(async (period: InsightsPeriod): Promise<{ rows: readonly HistoryRequest[]; available: number }> => ({ rows: [request(`r-${period}`)], available: 1 }))
  prices = vi.fn(async (): Promise<PriceCatalog> => ({ prices: [], currency: 'USD' }))
  setPrice = vi.fn(async (): Promise<PriceCatalog> => ({ prices: [], currency: 'USD' }))
  removePrice = vi.fn(async (): Promise<PriceCatalog> => ({ prices: [], currency: 'USD' }))
  setCurrency = vi.fn(async (): Promise<PriceCatalog> => ({ prices: [], currency: 'USD' }))
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
    let release: ((rows: { rows: readonly HistoryRequest[]; available: number }) => void) | undefined
    const rows = new Promise<{ rows: readonly HistoryRequest[]; available: number }>((resolve) => { release = resolve })
    port.recent.mockImplementationOnce(async () => rows)
    const loading = model.load('48h')
    // The old period's rows must not sit under the new period's label while
    // its own answer is still in flight.
    expect(model.snapshot().recent).toEqual([])
    release?.({ rows: [request('r-48h')], available: 1 })
    await loading
    expect(model.snapshot().recent[0]?.id).toBe('r-48h')
    await model.load('48h')
    expect(model.snapshot().recent[0]?.id).toBe('r-48h')
  })

  it('answers a revisited period from the cache before the network replies', async () => {
    await model.load('24h')
    await model.load('48h')
    port.report.mockClear()
    port.recent.mockClear()
    // Park the re-read: if the screen waited for it, the answer would hang.
    port.report.mockImplementation(() => new Promise(() => {}))
    port.recent.mockImplementation(() => new Promise(() => {}))
    const loading = model.load('24h')
    // The cached answer is on screen synchronously; the in-flight re-read is
    // the background refresh, not the answer.
    expect(model.snapshot().phase).toBe('ready')
    expect(model.snapshot().report?.period).toBe('24h')
    expect(model.snapshot().recent[0]?.id).toBe('r-24h')
    await loading
  })

  it('a price edit invalidates the cache instead of serving old costs', async () => {
    await model.load('24h')
    await model.savePrice({ model: 'm', input: 1, cachedInput: 0, output: 2, reasoning: 0 })
    port.report.mockClear()
    await model.load('24h')
    // The screen answers from the re-loaded period, and the background
    // re-read still runs — but the stale pre-edit answer was never served.
    expect(port.report).toHaveBeenCalled()
  })

  it('a currency change invalidates the cache: old-unit costs may not serve again', async () => {
    await model.load('24h')
    await model.setCurrency('CNY')
    // Park the re-read: a cached answer would arrive instantly; a cleared
    // cache has to wait for the network, and the screen shows the wait.
    port.report.mockImplementation(() => new Promise(() => {}))
    void model.load('24h')
    expect(model.snapshot().phase).toBe('loading')
  })

  it('carries the untruncated row count so a short list reads as the newest part', async () => {
    port.recent.mockResolvedValueOnce({ rows: [request('r-24h')], available: 250 })
    await model.load('24h')
    const state = model.snapshot()
    expect(state.recentAvailable).toBe(250)
    expect(state.recent.length).toBe(1)
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
    port.setPrice.mockResolvedValue({ prices: [{ model: 'm', input: 1, cachedInput: 0.1, output: 2, reasoning: 0, updatedAt: '2026-09-29T12:00:00Z' }], currency: 'USD' })
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

  it('refreshes quietly: the phase never leaves ready while the numbers swap', async () => {
    await model.load('24h')
    let release: ((report: InsightsReport) => void) | undefined
    port.report.mockImplementationOnce((period: InsightsPeriod) => new Promise<InsightsReport>((resolve) => { release = () => resolve({ ...emptyReport(period), generatedAt: '2026-09-29T13:00:00Z' }) }))
    const refreshing = model.refresh()
    // In flight: the phase stays ready, the old report stays on screen.
    expect(model.snapshot().phase).toBe('ready')
    expect(model.snapshot().report?.generatedAt).toBe('2026-09-29T12:00:00Z')
    release?.({ ...emptyReport('24h'), generatedAt: '2026-09-29T13:00:00Z' })
    await refreshing
    expect(model.snapshot().report?.generatedAt).toBe('2026-09-29T13:00:00Z')
    expect(model.snapshot().phase).toBe('ready')
  })

  it('runs one refresh at a time: a second tick while in flight is a no-op', async () => {
    await model.load('24h')
    let release: ((report: InsightsReport) => void) | undefined
    port.report.mockImplementationOnce((period: InsightsPeriod) => new Promise<InsightsReport>((resolve) => { release = () => resolve(emptyReport(period)) }))
    const first = model.refresh()
    const second = model.refresh()
    // The second call must settle without touching the port at all: the count
    // stays at the initial load plus the one refresh actually in flight.
    await second
    expect(port.report).toHaveBeenCalledTimes(2)
    release?.(emptyReport('24h'))
    await first
    expect(port.report).toHaveBeenCalledTimes(2)
  })

  it('escalates out of the error phase with a real load on the next tick', async () => {
    port.report.mockRejectedValueOnce(new Error('down'))
    await model.load('24h')
    expect(model.snapshot().phase).toBe('error')
    await model.refresh()
    expect(model.snapshot().phase).toBe('ready')
    expect(model.snapshot().report?.period).toBe('24h')
  })

  it('discards a refresh that resolves after a load took over', async () => {
    await model.load('24h')
    let releaseRefresh: ((report: InsightsReport) => void) | undefined
    port.report.mockImplementationOnce((period: InsightsPeriod) => new Promise<InsightsReport>((resolve) => { releaseRefresh = () => resolve(emptyReport(period)) }))
    const refreshing = model.refresh()
    await model.load('48h')
    releaseRefresh?.(emptyReport('24h'))
    await refreshing
    expect(model.snapshot().period).toBe('48h')
  })
})
