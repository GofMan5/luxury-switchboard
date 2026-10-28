import { describe, expect, it } from 'vitest'
import { filterLog, formatLog, type LogEntry } from './plugin'

const at = Date.UTC(2026, 0, 2, 3, 4, 5)
const entries: LogEntry[] = [
  { at, level: 'debug', text: 'cache warm' },
  { at, level: 'info', text: 'started' },
  { at, level: 'warn', text: 'slow answer' },
  { at, level: 'error', text: 'failed\r\nat step 2' },
]

describe('plugin log', () => {
  it('keeps the chosen level and everything more severe', () => {
    expect(filterLog(entries, 'debug')).toBe(entries)
    expect(filterLog(entries, 'info').map((entry) => entry.level)).toEqual(['info', 'warn', 'error'])
    expect(filterLog(entries, 'warn').map((entry) => entry.level)).toEqual(['warn', 'error'])
    expect(filterLog(entries, 'error').map((entry) => entry.level)).toEqual(['error'])
  })

  it('formats one aligned line per entry and keeps continuation lines with it', () => {
    expect(formatLog(entries)).toBe([
      '2026-01-02T03:04:05.000Z DEBUG cache warm',
      '2026-01-02T03:04:05.000Z INFO  started',
      '2026-01-02T03:04:05.000Z WARN  slow answer',
      '2026-01-02T03:04:05.000Z ERROR failed',
      '    at step 2',
      '',
    ].join('\n'))
    expect(formatLog([])).toBe('')
  })
})
