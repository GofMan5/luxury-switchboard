import { describe, expect, it } from 'vitest'
import type { StatusContribution } from '../shared/contracts/extensions'
import { STATUS_LIMIT, overflowSummary, readoutLimit, readoutOwner, splitReadouts } from './status-readouts'

const readout = (id: string, text = id): StatusContribution => ({ id, text })

describe('status readouts', () => {
  it('keeps four inline at full width, one when compact and none below the supported size', () => {
    expect(readoutLimit(false, false)).toBe(STATUS_LIMIT)
    expect(STATUS_LIMIT).toBe(4)
    expect(readoutLimit(true, false)).toBe(1)
    expect(readoutLimit(true, true)).toBe(0)
  })

  it('splits in contribution order and puts the rest behind the pill', () => {
    const status = ['a', 'b', 'c', 'd', 'e', 'f'].map((id) => readout(id))
    const split = splitReadouts(status, STATUS_LIMIT)
    expect(split.shown.map((entry) => entry.id)).toEqual(['a', 'b', 'c', 'd'])
    expect(split.hidden.map((entry) => entry.id)).toEqual(['e', 'f'])
    expect(splitReadouts(status.slice(0, 3), STATUS_LIMIT).hidden).toEqual([])
    expect(splitReadouts(status, 0).shown).toEqual([])
  })

  it('names every hidden readout in the pill summary', () => {
    expect(overflowSummary([readout('x', 'Queue 3')])).toBe('1 more readout: Queue 3')
    expect(overflowSummary([readout('x', 'Queue 3'), readout('y', 'p95 210 ms')])).toBe('2 more readouts: Queue 3, p95 210 ms')
  })

  it('derives the owner from the id up to its last colon', () => {
    expect(readoutOwner('plugin:acme.tools:latency')).toBe('plugin:acme.tools')
    expect(readoutOwner('appearance:theme')).toBe('appearance')
    expect(readoutOwner('bare')).toBe('')
  })
})
