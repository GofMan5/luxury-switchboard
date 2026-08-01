import { describe, expect, it } from 'vitest'
import { tunnelLimitsAreValid } from './tunnel-form'

describe('tunnelLimitsAreValid', () => {
  it('rejects blank unlimited fields and values outside runtime bounds', () => {
    expect(tunnelLimitsAreValid('8797', '', '0')).toBe(false)
    expect(tunnelLimitsAreValid('8797', '0', '')).toBe(false)
    expect(tunnelLimitsAreValid('8797', '1000001', '0')).toBe(false)
    expect(tunnelLimitsAreValid('8797', '120', '2048')).toBe(true)
    expect(tunnelLimitsAreValid('8797', '120', '0', 'v1.23456.0123456789abcdef0123456789abcdef0123456789abcdef')).toBe(true)
    expect(tunnelLimitsAreValid('8797', '120', '0', 'https://example.invalid/tunnel')).toBe(false)
  })
})
