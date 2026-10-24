// @vitest-environment jsdom

import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ActivityRequest } from '../domain/activity'
import type { ActivityModelState } from '../application/activity-model'

vi.mock('./useActivity', () => ({ useActivity: vi.fn() }))
vi.mock('../../../app/services', () => ({ useAppServices: vi.fn() }))

import { useAppServices } from '../../../app/services'
import ActivityPage from './ActivityPage'
import { useActivity } from './useActivity'

// Auto-cleanup needs vitest globals, which this project does not enable.
afterEach(cleanup)

const mockUseActivity = vi.mocked(useActivity)
const mockUseAppServices = vi.mocked(useAppServices)
const mockConnect = vi.fn()

const summary = { requests: 0, active: 0, queued: 0, successRate: 0, p95Ms: 0, rpm: 0 }

const request = (id: string, state: ActivityRequest['state'] = 'active'): ActivityRequest => ({
  id, startedAt: '2026-01-01T00:00:00Z', updatedAt: '2026-01-01T00:00:01Z', state,
  model: 'model', providerId: 'provider', providerName: 'Provider', method: 'POST',
  path: '/v1/responses', queueMs: 0, latencyMs: 0, retries: 0, bytesIn: 0, bytesOut: 0,
  inputTokens: 0, outputTokens: 0, cachedTokens: 0, reasoningTokens: 0,
  totalTokens: 0, contextTokens: 0, generationMs: 0, tokensPerSecond: 0,
})

function show(state: ActivityModelState) {
  mockUseActivity.mockReturnValue(state)
  render(<ActivityPage />)
}

beforeEach(() => {
  mockConnect.mockReset()
  mockUseAppServices.mockReturnValue({ activity: { connect: mockConnect } } as never)
})

describe('ActivityPage phases', () => {
  it('shows skeleton rows and a busy table while the first batch loads', () => {
    show({ phase: 'loading', requests: [], available: 0, summary, error: '' })
    // A live region's text is announced, not named, so the name filter stays off.
    expect(screen.getByRole('status').textContent).toMatch(/loading live activity/iu)
    expect(screen.getByRole('table').closest('div')?.getAttribute('aria-busy')).toBe('true')
    expect(screen.getByText('Connecting…')).toBeTruthy()
    expect(screen.queryByText(/showing/iu)).toBeNull()
  })

  it('shows the error with a retry that reconnects', () => {
    show({ phase: 'error', requests: [], available: 0, summary, error: 'Activity is unavailable' })
    expect(screen.getByRole('alert')).toBeTruthy()
    // The alert is the state; an empty-table row would double-report it.
    expect(screen.queryByText(/no requests match/iu)).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(mockConnect).toHaveBeenCalledOnce()
  })
})

describe('ActivityPage rows and inspector', () => {
  it('marks columns, selects with roving tabindex, and opens on Enter', async () => {
    show({ phase: 'ready', available: 2, requests: [request('a'), request('b', 'completed')], summary, error: '' })
    expect(document.querySelectorAll('th[scope="col"]')).toHaveLength(10)
    const rows = screen.getByRole('table').querySelectorAll('tbody tr[data-row]')
    expect(rows).toHaveLength(2)
    expect(rows[0].getAttribute('tabindex')).toBe('0')
    expect(rows[1].getAttribute('tabindex')).toBe('-1')
    expect(rows[0].getAttribute('aria-selected')).toBe('false')

    fireEvent.click(rows[1])
    expect(await screen.findByRole('complementary', { name: 'Request inspector' })).toBeTruthy()
    const selected = screen.getByRole('table').querySelectorAll('tbody tr[data-row]')
    expect(selected[1].getAttribute('aria-selected')).toBe('true')
    expect(selected[1].getAttribute('tabindex')).toBe('0')
  })

  it('keeps a visible keyboard entry when selection is filtered out, including empty and unfiltered results', () => {
    show({ phase: 'ready', available: 2, requests: [request('a'), request('b', 'completed')], summary, error: '' })
    const rows = () => [...screen.getByRole('table').querySelectorAll<HTMLElement>('tbody tr[data-row]')]
    fireEvent.click(rows()[1])
    fireEvent.click(screen.getByRole('button', { name: 'Live' }))
    expect(rows()).toHaveLength(1)
    expect(rows()[0].tabIndex).toBe(0)
    expect(screen.getByRole('complementary').textContent).toContain('b')
    fireEvent.click(screen.getByRole('button', { name: 'Cancelled' }))
    expect(rows()).toHaveLength(0)
    fireEvent.click(screen.getByRole('button', { name: 'All' }))
    expect(rows().map((row) => row.tabIndex)).toEqual([-1, 0])
    fireEvent.click(screen.getByRole('button', { name: 'Live' }))
    rows()[0].focus()
    fireEvent.keyDown(rows()[0], { key: 'Enter' })
    expect(rows()[0].getAttribute('aria-selected')).toBe('true')
  })

  it('falls back to the first live row after the selected request is evicted', () => {
    mockUseActivity.mockReturnValue({ phase: 'ready', available: 2, requests: [request('a'), request('b')], summary, error: '' })
    const view = render(<ActivityPage />)
    const rows = () => [...screen.getByRole('table').querySelectorAll<HTMLElement>('tbody tr[data-row]')]
    fireEvent.click(rows()[1])
    mockUseActivity.mockReturnValue({ phase: 'ready', available: 2, requests: [request('c'), request('a')], summary, error: '' })
    view.rerender(<ActivityPage />)
    expect(rows().map((row) => row.tabIndex)).toEqual([0, -1])
    rows()[0].focus()
    fireEvent.keyDown(rows()[0], { key: 'ArrowDown' })
    expect(document.activeElement).toBe(rows()[1])
    fireEvent.keyDown(rows()[1], { key: 'Enter' })
    expect(rows()[1].getAttribute('aria-selected')).toBe('true')
  })

  it('moves focus with arrows and returns it to the row when the inspector closes', async () => {
    show({ phase: 'ready', available: 2, requests: [request('a'), request('b')], summary, error: '' })
    const first = screen.getByRole('table').querySelectorAll('tbody tr[data-row]')[0] as HTMLElement
    first.focus()
    fireEvent.keyDown(first, { key: 'ArrowDown' })
    const rows = screen.getByRole('table').querySelectorAll('tbody tr[data-row]')
    expect(document.activeElement).toBe(rows[1])

    fireEvent.keyDown(rows[1], { key: 'Enter' })
    const dialog = await screen.findByRole('complementary', { name: 'Request inspector' })
    // Non-modal side panel: live updates continue behind it, so modal semantics would lie.
    expect((dialog as HTMLElement).getAttribute('role')).toBe('complementary')
    expect((dialog as HTMLElement).hasAttribute('aria-modal')).toBe(false)
    fireEvent.keyDown(dialog, { key: 'Escape' })
    expect(screen.queryByRole('complementary', { name: 'Request inspector' })).toBeNull()
    expect(document.activeElement).toBe(rows[1])
  })
})

describe('ActivityPage running time', () => {
  afterEach(() => vi.useRealTimers())

  it('ticks how long a live request has been running, and stops the clock on pause', () => {
    vi.useFakeTimers({ toFake: ['Date', 'setTimeout', 'clearTimeout'] })
    vi.setSystemTime(Date.parse('2026-01-01T00:00:07.400Z'))
    show({
      phase: 'ready', available: 2, summary, error: '',
      requests: [request('live'), { ...request('done', 'completed'), latencyMs: 3_210 }],
    })
    const cells = () => [...screen.getByRole('table').querySelectorAll<HTMLTableRowElement>('tbody tr[data-row]')].map((row) => row.cells[5].textContent)
    const finished = `${(3.21).toLocaleString(undefined, { minimumFractionDigits: 2 })} s`
    expect(cells()).toEqual(['7 s', finished])
    act(() => vi.advanceTimersByTime(600))
    expect(cells()).toEqual(['8 s', finished])
    act(() => vi.advanceTimersByTime(65_000))
    expect(cells()[0]).toBe('1m 13s')

    fireEvent.click(screen.getByRole('button', { name: 'Pause' }))
    act(() => vi.advanceTimersByTime(10_000))
    expect(cells()[0]).toBe('1m 13s')
    fireEvent.click(screen.getByRole('button', { name: 'Resume' }))
    expect(cells()[0]).toBe('1m 23s')

    fireEvent.click(screen.getAllByRole('row')[1])
    expect(screen.getByRole('complementary').textContent).toContain('Running for1m 23s')
  })
})

describe('ActivityPage pace', () => {
  const rows = () => [...screen.getByRole('table').querySelectorAll<HTMLTableRowElement>('tbody tr[data-row]')]

  it('shows the first token and the measured decode rate in the inspector', () => {
    show({
      phase: 'ready', available: 1, summary, error: '',
      requests: [{ ...request('a', 'completed'), latencyMs: 3_000, ttftMs: 412, tokensPerSecond: 85.5 }],
    })
    fireEvent.click(rows()[0])
    const inspector = screen.getByRole('complementary', { name: 'Request inspector' })
    expect(inspector.textContent).toContain('First token412 ms')
    expect(inspector.textContent).toMatch(/Generation85[.,]5 tok\/s/u)
  })

  it('shows a dash before the first token and for an older sidecar', () => {
    show({ phase: 'ready', available: 1, summary, error: '', requests: [request('a')] })
    fireEvent.click(rows()[0])
    const inspector = screen.getByRole('complementary', { name: 'Request inspector' })
    expect(inspector.textContent).toContain('First token—')
    expect(inspector.textContent).toContain('Generation—')
  })
})

describe('ActivityPage reasoning', () => {
  const rows = () => [...screen.getByRole('table').querySelectorAll<HTMLTableRowElement>('tbody tr[data-row]')]

  it('shows a live request in its reasoning phase as thinking, with the level it asked for', () => {
    show({
      phase: 'ready', available: 3, summary, error: '',
      requests: [
        { ...request('a'), reasoningEffort: 'high', thinking: true, thinkingMs: 0 },
        { ...request('b'), reasoningEffort: 'on', reasoningBudget: 16_000, thinking: false, thinkingMs: 2_400 },
        request('c'),
      ],
    })
    // No new column: the level rides beside the model.
    expect(document.querySelectorAll('th[scope="col"]')).toHaveLength(10)
    const [thinking, answering, plain] = rows()
    expect(thinking.cells[0].textContent).toBe('Thinking')
    expect(thinking.cells[1].textContent).toBe('modelReasoning High')
    expect(answering.cells[0].textContent).toBe('Streaming')
    expect(answering.cells[1].textContent).toBe('modelReasoning 16K')
    expect(plain.cells[0].textContent).toBe('Streaming')
    expect(plain.cells[1].textContent).toBe('model')

    fireEvent.click(answering)
    const inspector = screen.getByRole('complementary', { name: 'Request inspector' })
    expect(inspector.textContent).toContain(`Reasoning${(16_000).toLocaleString()}-token budget · ${(2.4).toLocaleString(undefined, { minimumFractionDigits: 2 })} s thinking`)
  })

  it('never shows a finished request as thinking, even if its last frame said so', () => {
    show({ phase: 'ready', available: 1, summary, error: '', requests: [{ ...request('a', 'completed'), reasoningEffort: 'low', thinking: true, thinkingMs: 900 }] })
    expect(rows()[0].cells[0].textContent).toBe('Complete')
    fireEvent.click(rows()[0])
    expect(screen.getByRole('complementary').textContent).toContain('ReasoningLow effort · 900 ms thinking')
  })

  it('reads a request from an older sidecar as the provider default', () => {
    show({ phase: 'ready', available: 1, summary, error: '', requests: [request('a')] })
    fireEvent.click(rows()[0])
    expect(screen.getByRole('complementary').textContent).toContain('ReasoningProvider default')
  })
})
