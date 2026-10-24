import type { ContributionTone } from '../../../shared/contracts/extensions'

/**
 * The message protocol between a plugin's worker and the host window. The
 * worker is untrusted: every message it sends is checked here before the
 * host acts on it, and anything that does not match is a protocol violation
 * that stops the plugin.
 */
export const PAGE_ID_PATTERN = /^[a-z0-9][a-z0-9_-]{0,47}$/u
/** Command and status ids, local to their plugin. */
export const LOCAL_ID_PATTERN = /^[a-z0-9][a-z0-9_.-]{0,47}$/u
export const STORAGE_KEY_PATTERN = /^[A-Za-z0-9._:-]{1,64}$/u
export const STORAGE_VALUE_BYTES = 8192
export const STORAGE_KEYS = 64
export const STORAGE_TOTAL_BYTES = 65_536

export const RUNTIME_LIMITS = {
  pages: 8,
  commands: 32,
  status: 3,
  topics: 24,
  inflight: 16,
  pageTitle: 48,
  commandTitle: 80,
  statusText: 40,
  tooltip: 200,
  notifyTitle: 120,
  notifyBody: 1000,
  logText: 2000,
  /** Notifications a plugin may raise per minute. */
  notifyPerMinute: 6,
} as const

export const OPS = [
  'ui.registerPage',
  'ui.unregisterPage',
  'ui.render',
  'ui.setStatus',
  'commands.register',
  'commands.unregister',
  'notify',
  'control.call',
  'events.subscribe',
  'events.unsubscribe',
  'storage.get',
  'storage.set',
  'storage.remove',
  'storage.all',
  'navigate',
] as const

export type Op = (typeof OPS)[number]
export type LogLevel = 'debug' | 'info' | 'warn' | 'error'

export type WorkerMessage =
  | { readonly type: 'ready' }
  | { readonly type: 'fail'; readonly message: string }
  | { readonly type: 'fault'; readonly message: string }
  | { readonly type: 'log'; readonly level: LogLevel; readonly text: string }
  | { readonly type: 'pong'; readonly seq: number }
  | { readonly type: 'call'; readonly id: number; readonly op: Op; readonly args: Record<string, unknown> }

/** What a page sends back when the operator touches it. */
export interface PageEvent {
  readonly kind: 'action' | 'change' | 'show' | 'hide'
  readonly action?: string
  readonly name?: string
  readonly value?: string | boolean
  /** Table row id (or index) for a row action. */
  readonly row?: string | number
  /** Every named control's current value on the page. */
  readonly form: Readonly<Record<string, string | boolean>>
}

export type HostMessage =
  | { readonly type: 'activate' }
  | { readonly type: 'result'; readonly id: number; readonly ok: true; readonly value: unknown }
  | { readonly type: 'result'; readonly id: number; readonly ok: false; readonly error: { readonly code: string; readonly message: string } }
  | { readonly type: 'ui'; readonly pageId: string; readonly event: PageEvent }
  | { readonly type: 'topic'; readonly topic: string; readonly payload: unknown }
  | { readonly type: 'command'; readonly id: string }
  | { readonly type: 'ping'; readonly seq: number }

const LEVELS: readonly LogLevel[] = ['debug', 'info', 'warn', 'error']
const OP_SET = new Set<string>(OPS)

/** The worker message, or null when it is not one. */
export function parseWorkerMessage(data: unknown): WorkerMessage | null {
  if (!isRecord(data)) return null
  switch (data.type) {
    case 'ready': return { type: 'ready' }
    case 'fail':
    case 'fault':
      return typeof data.message === 'string' ? { type: data.type, message: clip(data.message, RUNTIME_LIMITS.logText) } : null
    case 'log':
      return typeof data.text === 'string' && LEVELS.includes(data.level as LogLevel)
        ? { type: 'log', level: data.level as LogLevel, text: clip(data.text, RUNTIME_LIMITS.logText) }
        : null
    case 'pong':
      return Number.isSafeInteger(data.seq) ? { type: 'pong', seq: data.seq as number } : null
    case 'call':
      if (!Number.isSafeInteger(data.id) || (data.id as number) < 0) return null
      if (typeof data.op !== 'string' || !OP_SET.has(data.op)) return null
      if (data.args !== undefined && !isRecord(data.args)) return null
      return { type: 'call', id: data.id as number, op: data.op as Op, args: (data.args ?? {}) as Record<string, unknown> }
    default:
      return null
  }
}

/** A refused call: the plugin sees `error.code` and `error.message`. */
export class CallError extends Error {
  readonly code: string
  constructor(code: string, message: string) {
    super(message)
    this.name = 'CallError'
    this.code = code
  }
}

const TONES: readonly ContributionTone[] = ['neutral', 'info', 'success', 'warning', 'danger']

export interface PageSpec { readonly id: string; readonly title: string; readonly icon?: string }
export interface CommandSpec { readonly id: string; readonly title: string; readonly group?: string; readonly keywords?: readonly string[]; readonly shortcut?: string }
export interface StatusSpec { readonly text: string; readonly tone?: ContributionTone; readonly tooltip?: string }
export interface NotifySpec { readonly title: string; readonly body: string; readonly tone: ContributionTone }

export function pageSpec(args: Record<string, unknown>): PageSpec {
  const id = pattern(args.id, PAGE_ID_PATTERN, 'id', 'a page id: lowercase letters, digits, dashes and underscores, up to 48')
  const title = string(args.title, 'title', RUNTIME_LIMITS.pageTitle)
  if (args.icon !== undefined && (typeof args.icon !== 'string' || !/^[a-z]{1,24}$/u.test(args.icon))) throw new CallError('invalid_argument', 'icon must be one of the documented icon names.')
  return { id, title, ...(args.icon ? { icon: args.icon as string } : {}) }
}

export function commandSpec(args: Record<string, unknown>): CommandSpec {
  const id = pattern(args.id, LOCAL_ID_PATTERN, 'id', 'a command id: lowercase letters, digits, dots, dashes and underscores, up to 48')
  const title = string(args.title, 'title', RUNTIME_LIMITS.commandTitle)
  const group = args.group === undefined ? undefined : string(args.group, 'group', 32)
  let keywords: string[] | undefined
  if (args.keywords !== undefined) {
    if (!Array.isArray(args.keywords) || args.keywords.length > 8 || args.keywords.some((word) => typeof word !== 'string' || word.length === 0 || word.length > 32)) {
      throw new CallError('invalid_argument', 'keywords must be up to 8 words of at most 32 characters.')
    }
    keywords = args.keywords as string[]
  }
  const shortcut = args.shortcut === undefined ? undefined : string(args.shortcut, 'shortcut', 24)
  return { id, title, ...(group ? { group } : {}), ...(keywords ? { keywords } : {}), ...(shortcut ? { shortcut } : {}) }
}

export function statusSpec(args: Record<string, unknown>): { id: string; status: StatusSpec | null } {
  const id = pattern(args.id, LOCAL_ID_PATTERN, 'id', 'a status id: lowercase letters, digits, dots, dashes and underscores, up to 48')
  if (args.status === null || args.status === undefined) return { id, status: null }
  if (!isRecord(args.status)) throw new CallError('invalid_argument', 'status must be { text, tone?, tooltip? } or null.')
  const text = string(args.status.text, 'status.text', RUNTIME_LIMITS.statusText)
  const tone = args.status.tone === undefined ? undefined : tone_(args.status.tone, 'status.tone')
  const tooltip = args.status.tooltip === undefined ? undefined : string(args.status.tooltip, 'status.tooltip', RUNTIME_LIMITS.tooltip, true)
  return { id, status: { text, ...(tone ? { tone } : {}), ...(tooltip ? { tooltip } : {}) } }
}

export function notifySpec(args: Record<string, unknown>): NotifySpec {
  return {
    title: string(args.title, 'title', RUNTIME_LIMITS.notifyTitle),
    body: args.body === undefined ? '' : string(args.body, 'body', RUNTIME_LIMITS.notifyBody, true),
    tone: args.tone === undefined ? 'info' : tone_(args.tone, 'tone'),
  }
}

export function storageKey(args: Record<string, unknown>): string {
  return pattern(args.key, STORAGE_KEY_PATTERN, 'key', '1–64 letters, digits, dots, colons, dashes or underscores')
}

export function localId(args: Record<string, unknown>, field = 'id', shape: RegExp = LOCAL_ID_PATTERN): string {
  return pattern(args[field], shape, field, 'a valid id')
}

export function requireString(args: Record<string, unknown>, field: string, limit: number): string {
  return string(args[field], field, limit)
}

function pattern(value: unknown, shape: RegExp, field: string, description: string): string {
  if (typeof value !== 'string' || !shape.test(value)) throw new CallError('invalid_argument', `${field} must be ${description}.`)
  return value
}

function string(value: unknown, field: string, limit: number, allowEmpty = false): string {
  if (typeof value !== 'string' || (!allowEmpty && value.trim().length === 0)) throw new CallError('invalid_argument', `${field} must be a non-empty string.`)
  if (value.length > limit) throw new CallError('invalid_argument', `${field} is longer than ${limit} characters.`)
  return value
}

function tone_(value: unknown, field: string): ContributionTone {
  if (!TONES.includes(value as ContributionTone)) throw new CallError('invalid_argument', `${field} must be one of ${TONES.join(', ')}.`)
  return value as ContributionTone
}

function clip(value: string, limit: number): string {
  return value.length > limit ? `${value.slice(0, limit - 1)}…` : value
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
