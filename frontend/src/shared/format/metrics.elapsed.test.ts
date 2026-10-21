import { describe, expect, it } from 'vitest'
import { formatElapsed } from './metrics'

describe('formatElapsed', () => {
  it('reads as a running clock in whole units', () => {
    expect(formatElapsed(0)).toBe('0 s')
    expect(formatElapsed(999)).toBe('0 s')
    expect(formatElapsed(59_999)).toBe('59 s')
    expect(formatElapsed(60_000)).toBe('1m 00s')
    expect(formatElapsed(125_400)).toBe('2m 05s')
    expect(formatElapsed(3_780_000)).toBe('1h 03m')
    expect(formatElapsed(-5)).toBe('0 s')
    expect(formatElapsed(Number.NaN)).toBe('0 s')
  })
})
