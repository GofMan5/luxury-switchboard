import { describe, expect, it } from 'vitest'
import { requestRuleProblems, retryRuleProblems, ruleTestProblems, type RequestRule, type RetryRule } from '../domain/rules'
import {
  duplicateHeader, editorProblems, headerRow, problemFinder, requestRuleForm, requestRuleFromForm, retryRuleForm, retryRuleFromForm,
  splitList, suggestedId, testInput,
} from './rule-forms'

const fullRule: RequestRule = {
  id: 'everything', name: 'Everything', enabled: false, owner: 'plugin:team-tags',
  match: { models: ['gpt-4*', 'glm-*'], endpoints: ['chat', 'responses'] },
  effects: {
    provider: 'sigma', model: 'glm-5.3',
    block: { status: 429, message: 'Slow down.' },
    rateLimit: { requests: 10, windowSeconds: 7_200 },
    budget: { tokens: 50_000, period: 'hour' },
    headers: { 'X-Team': 'research', 'X-Cost-Centre': 'r&d 7' },
    tags: ['drafts', 'team.a'],
    stop: true,
  },
}

const retry: RetryRule = {
  id: 'overloaded', name: 'Overloaded', enabled: true, owner: '',
  match: { statuses: [503, 529], contains: ['overloaded, try later', 'busy'], errorCodes: ['upstream_status'], providers: ['north'], models: ['glm-*'] },
  action: 'switch_provider', maxAttempts: 2, delayMs: 250,
}

describe('splitList', () => {
  it('trims, drops blanks and repeats, keeps order', () => {
    expect(splitList(' a, b\n\n a ,c,')).toEqual(['a', 'b', 'c'])
    expect(splitList('one, two\nthree', /\n/u)).toEqual(['one, two', 'three'])
  })
})

describe('request-rule form', () => {
  it('round-trips every effect, the owner and the switch', () => {
    const form = requestRuleForm(fullRule)
    expect(form).toMatchObject({ rateWindow: '2', rateUnit: 'hour', budgetRequests: '', budgetTokens: '50000', owner: 'plugin:team-tags' })
    expect(requestRuleFromForm(form)).toEqual(fullRule)
  })

  it('starts a new rule enabled, matching everything and doing nothing', () => {
    const form = requestRuleForm(null)
    expect(form).toMatchObject({ enabled: true, owner: '', rateWindow: '1', rateUnit: 'minute', blockStatus: 403, budgetPeriod: 'day' })
    expect(requestRuleFromForm({ ...form, id: 'new', name: 'New' })).toEqual({
      id: 'new', name: 'New', enabled: true, owner: '', match: { models: [], endpoints: [] }, effects: {},
    })
  })

  it('leaves switched-off effects out and blank header rows behind', () => {
    const form = { ...requestRuleForm(fullRule), blockOn: false, rateOn: false, budgetOn: false, stop: false, headers: [headerRow('', ' '), headerRow('X-Team', 'a')] }
    expect(requestRuleFromForm(form).effects).toEqual({ provider: 'sigma', model: 'glm-5.3', headers: { 'X-Team': 'a' }, tags: ['drafts', 'team.a'] })
  })

  it('turns typed numbers the domain refuses into named problems', () => {
    const form = { ...requestRuleForm(null), id: 'n', name: 'N', rateOn: true, rateRequests: '', rateWindow: 'ten', budgetOn: true }
    const fields = requestRuleProblems(requestRuleFromForm(form)).map((problem) => problem.field)
    expect(fields).toEqual(['rateRequests', 'rateWindow', 'budget'])
  })

  it('catches a header listed twice before it collapses into one key', () => {
    const form = { ...requestRuleForm(null), headers: [headerRow('X-Team', 'a'), headerRow(' x-team ', 'b')] }
    expect(duplicateHeader(form)).toBe('x-team')
    expect(duplicateHeader(requestRuleForm(fullRule))).toBe('')
  })
})

describe('retry-rule form', () => {
  it('round-trips a rule, one phrase per line', () => {
    const form = retryRuleForm(retry)
    expect(form.contains).toBe('overloaded, try later\nbusy')
    expect(retryRuleFromForm(form)).toEqual(retry)
  })

  it('prefills a new rule from a failed request', () => {
    const form = retryRuleForm(null, { status: 503, errorCode: 'upstream_status', detail: 'The model is overloaded' })
    expect(form).toMatchObject({ name: 'Handle 503 upstream_status', statuses: '503', errorCodes: 'upstream_status', contains: 'The model is overloaded', action: 'retry', maxAttempts: '3', delayMs: '1000' })
    expect(retryRuleForm(null, { status: 429 }).delayMs).toBe('5000')
    expect(retryRuleForm(null, {}).name).toBe('Handle this error')
    expect(retryRuleForm(null).name).toBe('')
  })

  it('sends a failing rule one attempt without delay, whatever was typed', () => {
    const form = { ...retryRuleForm(retry), action: 'fail' as const, maxAttempts: 'many', delayMs: '' }
    expect(retryRuleFromForm(form)).toMatchObject({ action: 'fail', maxAttempts: 1, delayMs: 0 })
    expect(retryRuleProblems(retryRuleFromForm(form))).toEqual([])
  })

  it('names a status that is not a number', () => {
    const rule = retryRuleFromForm({ ...retryRuleForm(retry), statuses: '503 abc' })
    expect(retryRuleProblems(rule).map((problem) => problem.field)).toEqual(['statuses'])
  })
})

describe('editor problems', () => {
  it('puts a taken id first and adds a duplicate header once', () => {
    const problems = editorProblems([{ field: 'name', message: 'Name it.' }], { idTaken: true, duplicateHeader: 'X-Team' })
    expect(problems.map((problem) => problem.field)).toEqual(['id', 'name', 'headers'])
    const already = editorProblems([{ field: 'headers', message: 'X-Team is listed twice.' }], { duplicateHeader: 'x-team' })
    expect(already).toHaveLength(1)
  })

  it('stays quiet until the operator tries to save', () => {
    const problems = [{ field: 'rateRequests', message: 'Allow 1–100,000 requests.' }]
    expect(problemFinder(problems, false)('rateRequests')).toBeUndefined()
    expect(problemFinder(problems, true)('rateWindow', 'rateRequests')).toBe('Allow 1–100,000 requests.')
    expect(problemFinder(problems, true)('name')).toBeUndefined()
  })

  it('suggests an id only for a named rule', () => {
    expect(suggestedId('  ', new Set())).toBe('')
    expect(suggestedId('Overloaded', new Set(['overloaded']))).toBe('overloaded-2')
  })
})

describe('tester input', () => {
  const form = { model: ' glm-5.3 ', path: ' /v1/chat/completions ', status: ' 503 ', errorCode: ' ', errorText: 'busy\n', providerId: 'north' }

  it('sends the error only from the retry tester, blanks left out', () => {
    expect(testInput(form, false)).toEqual({ model: 'glm-5.3', path: '/v1/chat/completions' })
    expect(testInput(form, true)).toEqual({ model: 'glm-5.3', path: '/v1/chat/completions', status: 503, errorText: 'busy\n', providerId: 'north' })
  })

  it('keeps a typed status that is not a number refusable', () => {
    const input = testInput({ ...form, status: '5o3' }, true)
    expect(Number.isNaN(input.status)).toBe(true)
    expect(ruleTestProblems(input).map((problem) => problem.field)).toEqual(['status'])
  })
})
