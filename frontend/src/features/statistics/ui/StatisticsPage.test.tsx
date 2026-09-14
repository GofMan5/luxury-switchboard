// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { StatisticsState } from '../application/statistics-model'
import type { StatisticsSnapshot } from '../domain/statistics'

vi.mock('./useStatistics', () => ({ useStatistics: vi.fn() }))

import StatisticsPage from './StatisticsPage'
import { useStatistics } from './useStatistics'

// Auto-cleanup needs vitest globals, which this project does not enable.
afterEach(cleanup)

const mockUseStatistics = vi.mocked(useStatistics)
const mockLoad = vi.fn()

const stats = {
  requests: 4, completed: 3, failed: 1, cancelled: 0, retries: 0,
  inputTokens: 10, outputTokens: 20, cachedTokens: 0, reasoningTokens: 0,
  processedTokens: 30, nonCachedTokens: 30, p95Ms: 120, tokensPerSecond: 0,
}

const base = {
  id: 'r1', startedAt: '2026-01-01T00:00:00Z', updatedAt: '2026-01-01T00:00:01Z',
  state: 'completed' as const, model: 'model', providerId: 'provider', providerName: 'Provider',
  method: 'POST', path: '/v1/responses', queueMs: 0, latencyMs: 5, retries: 0,
  bytesIn: 0, bytesOut: 0, inputTokens: 0, outputTokens: 0, cachedTokens: 0,
  reasoningTokens: 0, totalTokens: 0, contextTokens: 0, generationMs: 0, tokensPerSecond: 0,
}

function show(state: StatisticsState) {
  mockUseStatistics.mockReturnValue({ model: { load: mockLoad }, state } as never)
  render(<StatisticsPage />)
}

beforeEach(() => {
  mockLoad.mockReset()
})

describe('StatisticsPage loading', () => {
  it('renders skeleton metrics instead of zeros and locks period switching', () => {
    show({ phase: 'loading', period: '24h', snapshot: null, error: '' })
    // A live region's text is announced, not named, so the name filter stays off.
    expect(screen.getByRole('status').textContent).toMatch(/loading statistics/iu)
    // Zeros would read as a real measurement; the skeleton reads as "not yet".
    expect(screen.queryByText('0 completed · 0 failed')).toBeNull()
    for (const period of ['24h', '48h', '72h', 'All']) {
      const button = screen.getByRole('button', { name: period })
      expect((button as HTMLButtonElement).disabled).toBe(true)
      expect(button.getAttribute('aria-pressed')).toBe(period === '24h' ? 'true' : 'false')
    }
    expect(screen.getByRole('button', { name: 'Refreshing…' })).toBeTruthy()
  })
})

describe('StatisticsPage errors', () => {
  it('shows placeholders instead of zeros on a first-load failure', () => {
    show({ phase: 'error', period: '24h', snapshot: null, error: 'Statistics are unavailable' })
    expect(screen.getByRole('alert').textContent).toMatch(/unavailable/iu)
    // Zeros would read as a real measurement; placeholders read as "not yet".
    expect(screen.queryByText('0 completed · 0 failed')).toBeNull()
    expect(screen.getByText('No data yet')).toBeTruthy()
    expect(screen.queryByText(/no persisted requests/iu)).toBeNull()
    expect(screen.queryByRole('status')).toBeNull()
    // Recovery stays available: a failed first load must not lock the controls.
    expect((screen.getByRole('button', { name: 'Refresh' }) as HTMLButtonElement).disabled).toBe(false)
  })

  it('truncates a long diagnostic in the list but keeps it whole behind the tooltip', () => {
    const detail = `refused: ${'x'.repeat(500)}`
    const snapshot: StatisticsSnapshot = { stats, recent: [{ ...base, state: 'failed', errorDetail: detail }] }
    show({ phase: 'ready', period: '24h', snapshot, error: '' })
    const shown = screen.getByText(/refused:/u)
    expect(shown.textContent?.length ?? 0).toBeLessThan(detail.length)
    expect(shown.getAttribute('title')).toBe(detail)
    // The tooltip alone is pointer-only: keyboard and AT get the full text.
    expect(shown.getAttribute('tabindex')).toBe('0')
    expect(shown.getAttribute('aria-label')).toBe(detail)
  })

  it('does not spam reloads while a load is in flight', () => {
    const snapshot: StatisticsSnapshot = { stats, recent: [] }
    show({ phase: 'loading', period: '24h', snapshot, error: '' })
    fireEvent.click(screen.getByRole('button', { name: '24h' }))
    expect(mockLoad).not.toHaveBeenCalled()
  })
})
