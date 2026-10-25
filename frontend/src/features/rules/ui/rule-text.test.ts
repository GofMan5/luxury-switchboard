import { describe, expect, it } from 'vitest'
import type { RequestRule, RetryRule } from '../domain/rules'
import { formatInteger as n } from '../../../shared/format/metrics'
import {
  describeEffects, describeRequestMatch, describeRetryAction, describeRetryMatch, errorKind, ownerLabel, requestRuleCounters, retryRuleCounters,
} from './rule-text'

const names = new Map([['north', 'North Relay']])
const rule = (patch: Partial<RequestRule> = {}): RequestRule => ({ id: 'a', name: 'A', enabled: true, match: { models: [], endpoints: [] }, effects: {}, ...patch })
const retry = (patch: Partial<RetryRule> = {}): RetryRule => ({
  id: 'r', name: 'R', enabled: true,
  match: { statuses: [502, 503], contains: ['overloaded'], errorCodes: ['upstream_status'], providers: ['north', 'gone'], models: ['glm-*'] },
  action: 'retry', maxAttempts: 3, delayMs: 1_500, ...patch,
})

describe('rule text', () => {
  it('describes what a request rule matches', () => {
    expect(describeRequestMatch(rule())).toBe('Any model · any endpoint')
    expect(describeRequestMatch(rule({ match: { models: ['gpt-4*', 'glm-*'], endpoints: ['chat'] } }))).toBe('gpt-4*, glm-* · Chat completions')
  })

  it('describes effects in the relay\'s order with provider names', () => {
    expect(describeEffects({
      stop: true, tags: ['x', 'y'], headers: { 'X-A': '1' }, budget: { requests: 1_000, tokens: 2_000_000, period: 'day' },
      rateLimit: { requests: 5, windowSeconds: 3_600 }, model: 'glm-5.3', provider: 'north', block: { status: 403, message: 'No.' },
    }, names)).toEqual([
      'Block with 403', 'Send to North Relay', 'Use model glm-5.3', 'At most 5 per hour',
      `Budget ${n(1_000)} requests and ${n(2_000_000)} tokens per day`, 'Adds 1 header', 'Tags x, y', 'Stops here',
    ])
    expect(describeEffects({ provider: 'gone', rateLimit: { requests: 2, windowSeconds: 90 }, headers: { 'X-A': '1', 'X-B': '2' } }, names))
      .toEqual(['Send to gone', 'At most 2 per 90 s', 'Adds 2 headers'])
    expect(describeEffects({ rateLimit: { requests: 2, windowSeconds: 172_800 } }, names)).toEqual(['At most 2 per 2 days'])
  })

  it('describes a retry rule', () => {
    expect(describeRetryMatch(retry(), names)).toBe('502, 503 · “overloaded” · upstream_status · from North Relay, gone · for glm-*')
    expect(describeRetryAction(retry())).toMatch(new RegExp(`^.+ up to 3 times, ${n(1_500)} ms apart$`, 'u'))
    expect(describeRetryAction(retry({ maxAttempts: 1, delayMs: 0 }))).toMatch(/ once$/u)
    expect(describeRetryAction(retry({ action: 'fail' }))).toBe('Fail at once')
  })

  it('names a transport failure and a plugin owner', () => {
    expect(errorKind({ signature: 's', status: 0, errorCode: 'transport', detail: '', providerName: '', model: '', count: 1, lastAt: '' })).toEqual({ title: 'No response', code: 'transport' })
    expect(errorKind({ signature: 's', status: 529, errorCode: 'upstream_status', detail: '', providerName: '', model: '', count: 1, lastAt: '' }).title).toBe('529')
    expect(ownerLabel('plugin:team-tags')).toBe('Plugin · team-tags')
    expect(ownerLabel('')).toBe('')
  })

  it('counts a budget over a rate window, and nothing open as zero', () => {
    const both = rule({ effects: { rateLimit: { requests: 10, windowSeconds: 60 }, budget: { requests: 100, tokens: 5_000, period: 'hour' } } })
    expect(requestRuleCounters(both, { hits: 1, windowRequests: 12, windowTokens: 900 })).toEqual({ hits: '1 hit', window: `12 / 100 req · 900 / ${n(5_000)} tok this hour`, detail: '' })
    const limited = rule({ effects: { rateLimit: { requests: 10, windowSeconds: 60 } } })
    expect(requestRuleCounters(limited, { hits: 1_284, windowRequests: 7, windowTokens: 0 })).toEqual({ hits: `${n(1_284)} hits`, window: '0 / 10 per minute', detail: '' })
    expect(requestRuleCounters(rule(), undefined)).toEqual({ hits: '0 hits', window: '', detail: '' })
    expect(retryRuleCounters(undefined)).toEqual({ hits: '0 hits', window: '', detail: '' })
  })

  it('shows when a window resets and when a rule last matched', () => {
    const limited = rule({ effects: { rateLimit: { requests: 10, windowSeconds: 60 } } })
    const counters = requestRuleCounters(limited, { hits: 3, windowRequests: 3, windowTokens: 0, windowResetsAt: '2026-01-01T12:34:56Z' })
    expect(counters.window).toBe('3 / 10 per minute')
    expect(counters.detail).toMatch(/^resets \S/u)
    expect(retryRuleCounters({ hits: 2, windowRequests: 0, windowTokens: 0, lastHitAt: '2026-01-01T12:34:56Z' }).detail).toMatch(/^last \S/u)
  })
})
