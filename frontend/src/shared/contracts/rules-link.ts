/**
 * Deep links into the Rules workspace. Another slice (the Activity inspector)
 * builds a link from a failed request; the Rules page reads it back and opens
 * a prefilled retry-rule editor. Both ends validate: a hand-typed hash can
 * carry anything, and the workspace never trusts it beyond these shapes.
 */

export type RulesTab = 'request' | 'retry' | 'errors'
export const RULES_TABS: readonly RulesTab[] = ['request', 'retry', 'errors']

/** What a failed request tells a new retry rule. Every field is optional. */
export interface RetryRuleDraftSource {
  readonly status?: number
  readonly errorCode?: string
  /** The already-redacted error detail; trimmed to DRAFT_TEXT_LIMIT. */
  readonly detail?: string
}

export interface RulesLink {
  readonly tab: RulesTab
  /** Present when the link asks for a new retry rule. */
  readonly draft: RetryRuleDraftSource | null
}

/** A retry rule's `contains` entry is 2–120 characters. */
export const DRAFT_TEXT_LIMIT = 120
const CODE_PATTERN = /^[a-z][a-z0-9_]{0,63}$/u
// eslint-disable-next-line no-control-regex -- control characters are exactly what this drops
const CONTROL = /[\u0000-\u001f\u007f-\u009f]+/gu

function cleanStatus(value: unknown): number | undefined {
  const status = typeof value === 'string' && /^\d{3}$/u.test(value) ? Number(value) : value
  return typeof status === 'number' && Number.isInteger(status) && status >= 400 && status <= 599 ? status : undefined
}

function cleanCode(value: unknown): string | undefined {
  return typeof value === 'string' && CODE_PATTERN.test(value) ? value : undefined
}

function cleanText(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const text = [...value.replace(CONTROL, ' ').replace(/\s+/gu, ' ').trim()].slice(0, DRAFT_TEXT_LIMIT).join('').trim()
  return text.length >= 2 ? text : undefined
}

/** Keeps only what a new retry rule can use: a 4xx/5xx status, a valid error
 * code and 2–120 characters of single-line text. */
export function cleanDraft(source: { status?: unknown; errorCode?: unknown; detail?: unknown }): RetryRuleDraftSource {
  const status = cleanStatus(source.status)
  const errorCode = cleanCode(source.errorCode)
  const detail = cleanText(source.detail)
  return { ...(status !== undefined ? { status } : {}), ...(errorCode ? { errorCode } : {}), ...(detail ? { detail } : {}) }
}

/** `#rules?tab=retry&draft=1&status=502&code=upstream_status&text=…`. Fields
 * that are missing or not valid are left out; the link always opens the
 * retry tab with a new-rule editor. */
export function retryRuleDraftHash(source: RetryRuleDraftSource): string {
  const draft = cleanDraft(source)
  const query = new URLSearchParams({ tab: 'retry', draft: '1' })
  if (draft.status !== undefined) query.set('status', String(draft.status))
  if (draft.errorCode) query.set('code', draft.errorCode)
  if (draft.detail) query.set('text', draft.detail)
  return `#rules?${query.toString()}`
}

/** A hash for one tab of the workspace. */
export function rulesTabHash(tab: RulesTab): string {
  return tab === 'request' ? '#rules' : `#rules?tab=${tab}`
}

/** Reads a Rules hash back; null when the hash is not the Rules route. */
export function parseRulesHash(hash: string): RulesLink | null {
  const value = hash.replace(/^#\/?/u, '')
  const at = value.indexOf('?')
  if ((at < 0 ? value : value.slice(0, at)) !== 'rules') return null
  const query = new URLSearchParams(at < 0 ? '' : value.slice(at + 1))
  const asked = query.get('tab')
  const tab = RULES_TABS.find((entry) => entry === asked) ?? 'request'
  if (tab !== 'retry' || query.get('draft') !== '1') return { tab, draft: null }
  return { tab, draft: cleanDraft({ status: query.get('status') ?? undefined, errorCode: query.get('code') ?? undefined, detail: query.get('text') ?? undefined }) }
}
