import { describe, expect, it } from 'vitest'
import { cleanDraft, DRAFT_TEXT_LIMIT, parseRulesHash, retryRuleDraftHash, rulesTabHash } from './rules-link'

describe('rules links', () => {
  it('round-trips a retry-rule draft through the hash', () => {
    const hash = retryRuleDraftHash({ status: 503, errorCode: 'upstream_status', detail: 'The model is overloaded & busy?' })
    expect(hash.startsWith('#rules?')).toBe(true)
    expect(parseRulesHash(hash)).toEqual({ tab: 'retry', draft: { status: 503, errorCode: 'upstream_status', detail: 'The model is overloaded & busy?' } })
  })

  it('drops the displayed HTTP status from transport drafts and links', () => {
    const draft = { errorCode: 'transport', detail: 'Connection reset' }
    expect(cleanDraft({ ...draft, status: 502 })).toEqual(draft)
    const hash = retryRuleDraftHash({ ...draft, status: 502 })
    expect(new URLSearchParams(hash.split('?')[1]).has('status')).toBe(false)
    expect(parseRulesHash(hash)).toEqual({ tab: 'retry', draft })
    expect(parseRulesHash('#rules?tab=retry&draft=1&status=502&code=transport')).toEqual({ tab: 'retry', draft: { errorCode: 'transport' } })
  })

  it('opens tabs and ignores other routes', () => {
    expect(parseRulesHash('#rules')).toEqual({ tab: 'request', draft: null })
    expect(parseRulesHash('#/rules?tab=errors')).toEqual({ tab: 'errors', draft: null })
    expect(parseRulesHash('#rules?tab=bogus')).toEqual({ tab: 'request', draft: null })
    expect(parseRulesHash('#rulesets')).toBeNull()
    expect(parseRulesHash('#settings?tab=retry')).toBeNull()
    expect(rulesTabHash('request')).toBe('#rules')
    expect(parseRulesHash(rulesTabHash('retry'))).toEqual({ tab: 'retry', draft: null })
  })

  it('only drafts on the retry tab', () => {
    expect(parseRulesHash('#rules?tab=request&draft=1&status=502')).toEqual({ tab: 'request', draft: null })
  })

  it('drops what a retry rule cannot use from a hand-typed hash', () => {
    const parsed = parseRulesHash('#rules?tab=retry&draft=1&status=200&code=Not-A-Code&text=x')
    expect(parsed).toEqual({ tab: 'retry', draft: {} })
  })

  it('keeps draft text single-line and within a contains entry', () => {
    expect(cleanDraft({ detail: 'line one\n\tline\u0007two  ' })).toEqual({ detail: 'line one line two' })
    const long = cleanDraft({ detail: 'é'.repeat(DRAFT_TEXT_LIMIT + 40) })
    expect([...(long.detail ?? '')]).toHaveLength(DRAFT_TEXT_LIMIT)
    expect(cleanDraft({ status: '429', errorCode: 'transport', detail: 7 })).toEqual({ errorCode: 'transport' })
    expect(cleanDraft({ status: '429', errorCode: 'upstream_status' })).toEqual({ status: 429, errorCode: 'upstream_status' })
    expect(cleanDraft({ status: 0 })).toEqual({})
  })
})
