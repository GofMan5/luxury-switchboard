// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from '@testing-library/react'
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
