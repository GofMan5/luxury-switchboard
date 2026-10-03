// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { InsightsState } from '../application/insights-model'
import type { InsightsReport, InsightsPeriod } from '../domain/insights'

vi.mock('./useInsights', () => ({ useInsights: vi.fn() }))

import InsightsPage from './InsightsPage'
import { useInsights } from './useInsights'

// Auto-cleanup needs vitest globals, which this project does not enable.
afterEach(cleanup)

const mockUseInsights = vi.mocked(useInsights)
const mockLoad = vi.fn()
const mockSavePrice = vi.fn()
const mockRemovePrice = vi.fn()

function emptyVolume() {
  return {
    requests: 0, completed: 0, failed: 0, cancelled: 0, retries: 0,
    inputTokens: 0, outputTokens: 0, cachedTokens: 0, reasoningTokens: 0,
    totalTokens: 0, generationMs: 0, cost: 0, isPriced: false,
  }
}

function sampleReport(): InsightsReport {
  return {
    period: '24h' as InsightsPeriod,
    generatedAt: '2026-09-28T01:30:38Z',
    overview: {
      volume: { ...emptyVolume(), requests: 20, completed: 15, failed: 5, retries: 2, totalTokens: 90_000, cost: 3.5, isPriced: true },
      successRate: 0.75, p50Ms: 200, p95Ms: 500, tokensPerSecond: 120, pricedRequests: 15, topErrorCode: 'upstream_unavailable',
    },
    providers: [
      { id: 'p1', name: 'Alpha', volume: { ...emptyVolume(), requests: 15, completed: 12, totalTokens: 60_000, cost: 3.5, isPriced: true }, p50Ms: 150, p95Ms: 400, avgMs: 180, tokensPerSecond: 100, errors: [] },
      { id: 'p2', name: 'Beta', volume: { ...emptyVolume(), requests: 5, completed: 3, failed: 2, totalTokens: 30_000 }, p50Ms: 250, p95Ms: 600, avgMs: 300, tokensPerSecond: 60, errors: [{ errorCode: 'upstream_unavailable', requests: 2 }] },
    ],
    models: [
      { model: 'gpt-6-astra', providerName: 'p1', volume: { ...emptyVolume(), requests: 20, completed: 15, totalTokens: 90_000, cost: 3.5, isPriced: true }, p50Ms: 200, p95Ms: 500, avgMs: 220, tokensPerSecond: 120, errors: [] },
      { model: 'claude-opus-5', providerName: 'p2', volume: { ...emptyVolume(), requests: 4, completed: 2, totalTokens: 10_000 }, p50Ms: 300, p95Ms: 700, avgMs: 350, tokensPerSecond: 40, errors: [] },
    ],
    daily: [
      { date: '2026-09-27', volume: { ...emptyVolume(), requests: 9, completed: 8 }, successRate: 8 / 9, tokensPerSecond: 0 },
      { date: '2026-09-28', volume: { ...emptyVolume(), requests: 11, completed: 7, cost: 3.5, isPriced: true }, successRate: 7 / 11, tokensPerSecond: 130 },
    ],
    errors: [{ errorCode: 'upstream_unavailable', requests: 4 }],
    unpricedModels: ['claude-opus-5'],
  }
}

const mockRefresh = vi.fn()

function show(state: InsightsState) {
  mockUseInsights.mockReturnValue({
    model: { load: mockLoad, savePrice: mockSavePrice, removePrice: mockRemovePrice, refresh: mockRefresh },
    state,
  } as never)
  render(<InsightsPage />)
}

beforeEach(() => {
  mockLoad.mockReset()
  mockSavePrice.mockReset().mockResolvedValue(true)
  mockRemovePrice.mockReset().mockResolvedValue(true)
})

describe('InsightsPage', () => {
  it('renders the report the model holds', () => {
    show({ phase: 'ready', period: '24h', report: sampleReport(), prices: [], pricesPhase: 'ready', error: '', recent: [], recentAvailable: 0 })
    expect(screen.getByText('Alpha')).toBeTruthy()
    // The model shows in the breakdown and in the pricing gap list; both are
    // the honest places for it.
    expect(screen.getAllByText('gpt-6-astra').length).toBeGreaterThan(0)
    expect(screen.getAllByText('$3.50').length).toBeGreaterThan(0)
    expect(screen.getAllByText(/claude-opus-5/).length).toBeGreaterThan(0)
  })

  it('shows the skeleton while loading and never a fake zero', () => {
    show({ phase: 'loading', period: '24h', report: null, prices: [], pricesPhase: 'idle', error: '', recent: [], recentAvailable: 0 })
    expect(screen.getByText('Loading insights…')).toBeTruthy()
    // Placeholders, not zeros: a first-load failure must not read as measured.
    expect(screen.queryByText('$0.00')).toBeNull()
  })

  it('surfaces the error state', () => {
    show({ phase: 'error', period: '24h', report: null, prices: [], pricesPhase: 'ready', error: 'Insights are unavailable', recent: [], recentAvailable: 0 })
    expect(screen.getByRole('alert').textContent).toContain('Insights are unavailable')
  })

  it('switches the period on demand', () => {
    show({ phase: 'ready', period: '24h', report: sampleReport(), prices: [], pricesPhase: 'ready', error: '', recent: [], recentAvailable: 0 })
    fireEvent.click(screen.getByRole('button', { name: '48h' }))
    expect(mockLoad).toHaveBeenCalledWith('48h')
  })

  it('opens the price editor and saves a draft through the model', async () => {
    show({ phase: 'ready', period: '24h', report: sampleReport(), prices: [], pricesPhase: 'ready', error: '', recent: [], recentAvailable: 0 })
    fireEvent.click(screen.getByRole('button', { name: /prices/i }))
    const modelInput = await screen.findByPlaceholderText('gpt-6-astra')
    fireEvent.change(modelInput, { target: { value: 'gpt-6-astra' } })
    // "Cached input" also matches a loose "Input" query, so the exact label
    // pins the field the draft is asserting about.
    const input = screen.getByLabelText('Input', { exact: true })
    fireEvent.change(input, { target: { value: '1.25' } })
    fireEvent.click(screen.getByRole('button', { name: /save price/i }))
    await waitFor(() => expect(mockSavePrice).toHaveBeenCalled())
    expect(mockSavePrice.mock.calls[0][0].model).toBe('gpt-6-astra')
    expect(mockSavePrice.mock.calls[0][0].input).toBe(1.25)
  })

  it('dismisses the price editor with Escape like every other modal', async () => {
    show({ phase: 'ready', period: '24h', report: sampleReport(), prices: [], pricesPhase: 'ready', error: '', recent: [], recentAvailable: 0 })
    fireEvent.click(screen.getByRole('button', { name: /prices/i }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.keyDown(dialog, { key: 'Escape' })
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })

  it('renders the persisted request rows behind the numbers', () => {
    show({
      phase: 'ready', period: '24h', report: sampleReport(), prices: [], pricesPhase: 'ready', error: '', recentAvailable: 1,
      recent: [{
        id: 'r1', state: 'failed', model: 'glm-5.3-prime', providerId: 'alpha-relay', status: 402,
        latencyMs: 79, totalTokens: 0, cachedTokens: 0, updatedAt: '2026-09-28T01:30:34Z',
        errorCode: 'balance_exhausted', errorDetail: 'Your balance has run out.',
      }],
    })
    const table = screen.getByRole('table', { name: 'Recent persisted requests' })
    expect(table.textContent).toContain('glm-5.3-prime')
    expect(table.textContent).toContain('402')
    // The diagnostic is its own section, not a tooltip nobody opens.
    expect(screen.getByText(/Your balance has run out/)).toBeTruthy()
  })

  it('names a bounded row list as the newest part, never as the whole journal', () => {
    show({
      phase: 'ready', period: '24h', report: sampleReport(), prices: [], pricesPhase: 'ready', error: '',
      recentAvailable: 250,
      recent: [{
        id: 'r1', state: 'failed', model: 'glm-5.3-prime', providerId: 'alpha-relay', status: 502,
        latencyMs: 30, totalTokens: 0, cachedTokens: 0, updatedAt: '2026-09-28T01:30:34Z',
        errorCode: 'upstream_status', errorDetail: '',
      }],
    })
    expect(screen.getByText('Newest 1 of 250 rows in the last 24h')).toBeTruthy()
  })

  it('sorts the model breakdown when a column header is clicked', () => {
    show({ phase: 'ready', period: '24h', report: sampleReport(), prices: [], pricesPhase: 'ready', error: '', recent: [], recentAvailable: 0 })
    const table = screen.getByRole('table', { name: 'Model breakdown' })
    const rows = () => [...table.querySelectorAll('tbody tr')].map((row) => row.querySelector('td')?.textContent)
    // Default sort: tokens, descending — gpt-6-astra (90k) before claude-opus-5 (10k).
    expect(rows()[0]).toBe('gpt-6-astra')
    // Click Model: alphabetical ascending.
    fireEvent.click(screen.getByRole('button', { name: 'Model' }))
    expect(rows()[0]).toBe('claude-opus-5')
    expect(table.querySelector('th[aria-sort="ascending"]')).toBeTruthy()
    // Click Model again: descending.
    fireEvent.click(screen.getByRole('button', { name: 'Model' }))
    expect(rows()[0]).toBe('gpt-6-astra')
    expect(table.querySelector('th[aria-sort="descending"]')).toBeTruthy()
  })
})
