// @vitest-environment jsdom

import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ModelTestResult } from '../../models/domain/model'
import type { TestsState } from '../application/tests-model'

vi.mock('../../../app/services', () => ({ useAppServices: vi.fn() }))
vi.mock('../../providers/ui/useProviders', () => ({ useProviders: vi.fn() }))

import { useAppServices } from '../../../app/services'
import { useProviders } from '../../providers/ui/useProviders'
import TestsPage from './TestsPage'

afterEach(cleanup)

const result = (model: string, extra: Partial<ModelTestResult>): ModelTestResult => ({
  runId: 'run', providerId: 'p1', model, state: 'available', status: 200, latencyMs: 2_000, ttftMs: 400, ...extra,
})

function show(results: readonly ModelTestResult[]) {
  const state: TestsState = {
    catalogs: { p1: results.map((entry) => entry.model) },
    results: Object.fromEntries(results.map((entry) => [`p1 ${entry.model}`, entry])),
    running: false, profile: 'short', runDone: 0, runTotal: 0, runFailed: 0, lastRunAt: '', error: '',
  }
  const tests = { subscribe: () => () => {}, snapshot: () => state, ensureCatalog: vi.fn(async () => []) }
  vi.mocked(useAppServices).mockReturnValue({ tests } as never)
  vi.mocked(useProviders).mockReturnValue({ state: { catalog: { providers: [{ id: 'p1', name: 'North Relay', enabled: true }] } } } as never)
  render(<TestsPage />)
}

const rateOf = (model: string) => {
  const row = screen.getByTitle(`${model} · North Relay`).closest('tr')
  return row?.cells[5]?.textContent
}

describe('TestsPage decode rate', () => {
  it('shows the relay-measured rate instead of re-deriving one from the totals', () => {
    // The old ratio would read 128 / 1.6 s = 80 tok/s here; the measurement
    // excludes the first token and says 61.4.
    show([result('measured', { outputTokens: 128, outputTps: 61.37 })])
    expect(rateOf('measured')).toBe('61.4')
  })

  it('shows a dash when the relay could not measure a rate', () => {
    // A buffered burst: tokens but no rate — the old ratio filed 80 tok/s.
    show([result('burst', { outputTokens: 128 }), result('older', { outputTokens: 128, outputTps: 0 })])
    expect(rateOf('burst')).toBe('—')
    expect(rateOf('older')).toBe('—')
  })
})
