import { describe, expect, it } from 'vitest'
import { describeReasoningLevel, formatReasoningLevel } from './reasoning'

describe('reasoning level', () => {
  it('renders each dialect spelling as a compact badge', () => {
    expect(formatReasoningLevel('high', undefined)).toBe('High')
    expect(formatReasoningLevel('xhigh', 0)).toBe('X-High')
    expect(formatReasoningLevel('none', undefined)).toBe('Off')
    expect(formatReasoningLevel('on', 16_000)).toBe('16K')
    expect(formatReasoningLevel('on', 2_048)).toBe('2K')
    expect(formatReasoningLevel('on', 1_536)).toBe('1.5K')
    expect(formatReasoningLevel('on', 512)).toBe('512')
    expect(formatReasoningLevel('low', 4_096)).toBe('Low · 4.1K')
    expect(formatReasoningLevel('auto', undefined)).toBe('Auto')
  })

  it('renders nothing for a request that left reasoning to the provider', () => {
    expect(formatReasoningLevel(undefined, undefined)).toBe('')
    expect(formatReasoningLevel('', 0)).toBe('')
    expect(formatReasoningLevel(undefined, Number.NaN)).toBe('')
    expect(describeReasoningLevel(undefined, undefined)).toBe('')
  })

  it('keeps a level from a newer sidecar visible and short', () => {
    expect(formatReasoningLevel('ultra', undefined)).toBe('ultra')
    expect(formatReasoningLevel('a-very-long-future-level', undefined)).toHaveLength(12)
  })

  it('describes the level in words for the inspector', () => {
    expect(describeReasoningLevel('high', undefined)).toBe('High effort')
    expect(describeReasoningLevel('on', 16_000)).toBe(`${(16_000).toLocaleString()}-token budget`)
    expect(describeReasoningLevel('on', undefined)).toBe('On')
    expect(describeReasoningLevel('auto', undefined)).toBe('Adaptive')
    expect(describeReasoningLevel('none', 2_048)).toBe('Off')
    expect(describeReasoningLevel('medium', 8_000)).toBe(`Medium effort · ${(8_000).toLocaleString()}-token budget`)
  })
})
