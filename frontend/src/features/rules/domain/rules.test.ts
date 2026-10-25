import { describe, expect, it } from 'vitest'
import {
  moveId, ownerPlugin, requestRuleProblems, retryRuleProblems, ruleIdFromName, ruleTestProblems, RULE_LIMITS,
  type RequestRule, type RetryRule,
} from './rules'

const requestRule = (patch: Partial<RequestRule> = {}): RequestRule => ({
  id: 'cheap-drafts', name: 'Cheap drafts', enabled: true,
  match: { models: ['gpt-4o-mini*'], endpoints: ['chat'] },
  effects: { provider: 'sigma', model: 'glm-5.3' },
  ...patch,
})

const retryRule = (patch: Partial<RetryRule> = {}): RetryRule => ({
  id: 'overloaded', name: 'Overloaded', enabled: true,
  match: { statuses: [503], contains: ['overloaded'], errorCodes: [], providers: [], models: [] },
  action: 'retry', maxAttempts: 3, delayMs: 500,
  ...patch,
})

const fields = (problems: readonly { field: string }[]) => problems.map((problem) => problem.field)

describe('requestRuleProblems', () => {
  it('accepts a complete rule with every effect', () => {
    expect(requestRuleProblems(requestRule({
      effects: {
        provider: 'sigma', model: 'glm-5.3',
        block: { status: 429, message: 'Slow down.' },
        rateLimit: { requests: 10, windowSeconds: 60 },
        budget: { requests: 100, tokens: 50_000, period: 'day' },
        headers: { 'X-Team': 'research' },
        tags: ['drafts', 'team.a'],
        stop: true,
      },
    }))).toEqual([])
  })

  it('names each refused field', () => {
    expect(fields(requestRuleProblems(requestRule({ id: 'Bad Id', name: ' ' })))).toEqual(['id', 'name'])
    expect(fields(requestRuleProblems(requestRule({ match: { models: Array.from({ length: RULE_LIMITS.globs + 1 }, (_, index) => `m${index}`), endpoints: [] } })))).toEqual(['models'])
    expect(fields(requestRuleProblems(requestRule({ match: { models: [], endpoints: ['chat', 'chat'] } })))).toEqual(['endpoints'])
    expect(fields(requestRuleProblems(requestRule({ effects: { block: { status: 500 as never, message: '' } } })))).toEqual(['blockStatus', 'blockMessage'])
    expect(fields(requestRuleProblems(requestRule({ effects: { rateLimit: { requests: 0, windowSeconds: 86_401 } } })))).toEqual(['rateRequests', 'rateWindow'])
    expect(fields(requestRuleProblems(requestRule({ effects: { budget: { period: 'day' } } })))).toEqual(['budget'])
    expect(fields(requestRuleProblems(requestRule({ effects: { budget: { tokens: 1.5, period: 'week' as never } } })))).toEqual(['budgetTokens', 'budgetPeriod'])
    expect(fields(requestRuleProblems(requestRule({ effects: { tags: ['Upper'] } })))).toEqual(['tags'])
  })

  it('refuses reserved, malformed and non-ASCII headers', () => {
    const messages = (headers: Record<string, string>) => requestRuleProblems(requestRule({ effects: { headers } })).map((problem) => problem.message)
    expect(messages({ 'X-Switchboard-Key': 'v' })[0]).toContain('reserved')
    expect(messages({ 'X-Forwarded-For': 'v' })[0]).toContain('reserved')
    expect(messages({ 'X-Api-Key': 'v' })[0]).toContain('reserved')
    expect(messages({ 'X-Goog-Api-Key': 'v' })[0]).toContain('reserved')
    expect(messages({ 'X-Stainless-Os': 'v' })[0]).toContain('reserved')
    expect(messages({ 'X-Team': 'v' })).toEqual([])
    expect(messages({ Authorization: 'v' })[0]).toContain('not a header name')
    expect(messages({ 'X-Team': 'naïve' })[0]).toContain('printable ASCII')
    expect(messages({ 'X-Team': '' })[0]).toContain('printable ASCII')
  })
})

describe('retryRuleProblems', () => {
  it('accepts a rule matching on a phrase alone', () => {
    expect(retryRuleProblems(retryRule({ match: { statuses: [], contains: ['quota'], errorCodes: [], providers: ['north'], models: ['glm-*'] } }))).toEqual([])
  })

  it('needs something to match on', () => {
    expect(fields(retryRuleProblems(retryRule({ match: { statuses: [], contains: [], errorCodes: [], providers: ['north'], models: [] } })))).toEqual(['match'])
  })

  it('names refused statuses, phrases, codes and numbers', () => {
    expect(fields(retryRuleProblems(retryRule({ match: { statuses: [200], contains: ['x'], errorCodes: ['Bad'], providers: [], models: [] } })))).toEqual(['statuses', 'contains', 'errorCodes'])
    expect(fields(retryRuleProblems(retryRule({ maxAttempts: 11, delayMs: -1 })))).toEqual(['maxAttempts', 'delayMs'])
    expect(fields(retryRuleProblems(retryRule({ action: 'pray' as never })))).toEqual(['action'])
  })
})

describe('ruleTestProblems', () => {
  it('needs a path and keeps error fields in range', () => {
    expect(ruleTestProblems({ model: 'glm-5.3', path: '/v1/chat/completions' })).toEqual([])
    expect(fields(ruleTestProblems({ model: '', path: ' ' }))).toEqual(['path'])
    expect(fields(ruleTestProblems({ model: '', path: '/v1/x', status: 302, errorCode: 'Upstream', providerId: 'p'.repeat(65) }))).toEqual(['status', 'errorCode', 'providerId'])
    expect(fields(ruleTestProblems({ model: '', path: '/v1/x', status: Number.NaN }))).toEqual(['status'])
  })

  it('lets pasted error text keep tabs and newlines, nothing else invisible', () => {
    expect(ruleTestProblems({ model: '', path: '/v1/x', errorText: 'line one\n\tline two' })).toEqual([])
    expect(fields(ruleTestProblems({ model: '', path: '/v1/x', errorText: 'bell\u0007' }))).toEqual(['errorText'])
    expect(fields(ruleTestProblems({ model: '', path: '/v1/x', errorText: '😀'.repeat(RULE_LIMITS.testErrorText + 1) }))).toEqual(['errorText'])
    expect(ruleTestProblems({ model: '', path: '/v1/x', errorText: '😀'.repeat(RULE_LIMITS.testErrorText) })).toEqual([])
  })
})

describe('rule helpers', () => {
  it('reads the plugin behind an owner', () => {
    expect(ownerPlugin('plugin:team-tags')).toBe('team-tags')
    expect(ownerPlugin('')).toBe('')
    expect(ownerPlugin(undefined)).toBe('')
  })

  it('moves an id one step and leaves the ends alone', () => {
    const ids = ['a', 'b', 'c']
    expect(moveId(ids, 'b', -1)).toEqual(['b', 'a', 'c'])
    expect(moveId(ids, 'b', 1)).toEqual(['a', 'c', 'b'])
    expect(moveId(ids, 'a', -1)).toBe(ids)
    expect(moveId(ids, 'c', 1)).toBe(ids)
    expect(moveId(ids, 'z', 1)).toBe(ids)
  })

  it('derives a free id from a name', () => {
    expect(ruleIdFromName('Cheap drafts!', new Set())).toBe('cheap-drafts')
    expect(ruleIdFromName('Cheap drafts', new Set(['cheap-drafts', 'cheap-drafts-2']))).toBe('cheap-drafts-3')
    expect(ruleIdFromName('Ünïcode ✓', new Set())).toBe('unicode')
    expect(ruleIdFromName('✓✓', new Set())).toBe('rule')
    expect(ruleIdFromName('x'.repeat(80), new Set())).toHaveLength(40)
  })
})
