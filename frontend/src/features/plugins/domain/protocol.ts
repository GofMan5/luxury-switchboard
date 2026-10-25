import type { BadgeContribution, ContributionTone } from '../../../shared/contracts/extensions'
import { parseShortcut } from '../../../shared/keyboard/shortcuts'
import { SETTING_PREFIX, type SettingValue } from './settings'

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
  badgeText: 8,
  confirmText: 24,
  clipboardText: 32_768,
  logText: 2000,
  /** Notifications a plugin may raise per minute. */
  notifyPerMinute: 6,
  /** Storage writes a plugin may make per minute; each one is an encrypted
   * save of the plugin store. */
  storageWritesPerMinute: 120,
  /** Entries in one storage.setMany. */
  storageBatch: 64,
  /** How long after the operator touched a plugin it may ask for
   * confirmation or write to the clipboard. */
  gestureMs: 10_000,
} as const

export const OPS = [
  'ui.registerPage',
  'ui.unregisterPage',
  'ui.render',
  'ui.setStatus',
  'ui.setBadge',
  'ui.confirm',
  'commands.register',
  'commands.unregister',
  'notify',
  'clipboard.writeText',
  'control.call',
  'events.subscribe',
  'events.unsubscribe',
  'storage.get',
  'storage.set',
  'storage.remove',
  'storage.all',
  'storage.setMany',
  'storage.clear',
  'navigate',
  'ui.registerWidget',
  'ui.registerInspectorTab',
  'ui.registerSettingsSection',
  'ui.registerAction',
  'ui.registerHeaderButton',
  'ui.unregister',
  'ui.setLayout',
  'ui.resetLayout',
  'ui.dialog',
  'ui.dialogUpdate',
  'ui.dialogClose',
  'ui.theme',
  'net.fetch',
  'db.get',
  'db.put',
  'db.delete',
  'db.list',
  'db.clear',
  'db.stats',
  'files.open',
  'files.save',
  'bus.publish',
  'bus.subscribe',
  'bus.unsubscribe',
  'relay.register',
  'relay.unregister',
] as const

export type Op = (typeof OPS)[number]
export type LogLevel = 'debug' | 'info' | 'warn' | 'error'

export type WorkerMessage =
  | { readonly type: 'ready' }
  | { readonly type: 'fail'; readonly message: string }
  /** An uncaught error after start-up; `command` names the command whose
   * handler threw, so the operator who ran it hears about it. */
  | { readonly type: 'fault'; readonly message: string; readonly command?: string }
  | { readonly type: 'log'; readonly level: LogLevel; readonly text: string }
  | { readonly type: 'pong'; readonly seq: number }
  | { readonly type: 'call'; readonly id: number; readonly op: Op; readonly args: Record<string, unknown> }
  /* Fire-and-forget messages: no result, validated by the host when used. */
  | { readonly type: 'draw'; readonly surface: string; readonly canvas: string; readonly ops: unknown }
  | { readonly type: 'tone'; readonly spec: Record<string, unknown> }
  | { readonly type: 'frameDone' }
  | { readonly type: 'hookResult'; readonly callId: string; readonly decision: unknown }

/** Worker messages that carry no result and are budgeted separately from
 * the call flood window. */
export const STREAM_MESSAGES: ReadonlySet<string> = new Set(['draw', 'tone', 'frameDone', 'hookResult'])

/** A form value: text, a slider's number or a toggle. */
export type FormValue = string | number | boolean

/** What a page, slot surface or dialog sends back when the operator touches
 * it. The host fills `pageId` (and `scope`/`target` for registered actions)
 * itself before the worker sees it. */
export interface PageEvent {
  readonly kind: 'action' | 'change' | 'show' | 'hide'
  readonly action?: string
  readonly name?: string
  readonly value?: FormValue
  /** Table row id (or index) for a row action. */
  readonly row?: string | number
  /** For an inspector tab: what it is showing. */
  readonly context?: { readonly scope: 'overview' | 'activity' | 'provider' | 'settings'; readonly id?: string }
  /** The page, `widget:`/`inspector:`/`settings:` surface or `dialog:<id>`
   * the event came from. */
  readonly pageId?: string
  /** For a registered action or header button. */
  readonly scope?: 'activity' | 'provider' | 'key' | 'header'
  readonly target?: string
  /** Every named control's current value on the page. */
  readonly form: Readonly<Record<string, FormValue>>
}

export type HostMessage =
  | { readonly type: 'activate' }
  | { readonly type: 'result'; readonly id: number; readonly ok: true; readonly value: unknown }
  | { readonly type: 'result'; readonly id: number; readonly ok: false; readonly error: { readonly code: string; readonly message: string } }
  | { readonly type: 'ui'; readonly pageId: string; readonly event: PageEvent }
  | { readonly type: 'topic'; readonly topic: string; readonly payload: unknown }
  | { readonly type: 'command'; readonly id: string }
  | { readonly type: 'settings'; readonly values: Readonly<Record<string, SettingValue>>; readonly secrets: readonly string[] }
  | { readonly type: 'ping'; readonly seq: number }
  | { readonly type: 'frame'; readonly dtMs: number; readonly tMs: number }
  | { readonly type: 'input'; readonly event: unknown }
  | { readonly type: 'hook'; readonly kind: 'invoke' | 'observe'; readonly hookId: string; readonly callId?: string; readonly request: unknown; readonly response?: unknown }
  | { readonly type: 'bus'; readonly channel: string; readonly payload: unknown; readonly from: string }
  | { readonly type: 'theme'; readonly theme: unknown }
  | { readonly type: 'dialog'; readonly id: string; readonly result: { readonly action: string | null; readonly form: Readonly<Record<string, FormValue>> } }

const LEVELS: readonly LogLevel[] = ['debug', 'info', 'warn', 'error']
const OP_SET = new Set<string>(OPS)

/** The worker message, or null when it is not one. */
export function parseWorkerMessage(data: unknown): WorkerMessage | null {
  if (!isRecord(data)) return null
  switch (data.type) {
    case 'ready': return { type: 'ready' }
    case 'fail':
      return typeof data.message === 'string' ? { type: 'fail', message: clip(data.message, RUNTIME_LIMITS.logText) } : null
    case 'fault':
      if (typeof data.message !== 'string') return null
      if (data.command !== undefined && (typeof data.command !== 'string' || !LOCAL_ID_PATTERN.test(data.command))) return null
      return { type: 'fault', message: clip(data.message, RUNTIME_LIMITS.logText), ...(data.command ? { command: data.command as string } : {}) }
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
    case 'draw':
      return typeof data.surface === 'string' && typeof data.canvas === 'string' && data.surface.length <= 64 && data.canvas.length <= 64
        ? { type: 'draw', surface: data.surface, canvas: data.canvas, ops: data.ops }
        : null
    case 'tone':
      return isRecord(data.spec) ? { type: 'tone', spec: data.spec } : null
    case 'frameDone': return { type: 'frameDone' }
    case 'hookResult':
      return typeof data.callId === 'string' && data.callId.length > 0 && data.callId.length <= 128
        ? { type: 'hookResult', callId: data.callId, decision: data.decision }
        : null
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
export interface ConfirmSpec { readonly title: string; readonly body: string; readonly confirmText: string; readonly tone: 'neutral' | 'danger' }

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
    if (!Array.isArray(args.keywords) || args.keywords.length > 8 || args.keywords.some((word) => typeof word !== 'string' || word.trim().length === 0 || runes(word) > 32)) {
      throw new CallError('invalid_argument', 'keywords must be up to 8 words of at most 32 characters.')
    }
    keywords = args.keywords as string[]
  }
  let shortcut: string | undefined
  if (args.shortcut !== undefined) {
    // A shortcut is a real key, never a decorative hint: it either binds or
    // the registration is refused.
    const parsed = typeof args.shortcut === 'string' ? parseShortcut(args.shortcut) : null
    if (!parsed?.ok) throw new CallError('invalid_argument', `shortcut: ${parsed ? parsed.problem : 'must be a string.'}`)
    shortcut = parsed.shortcut.text
  }
  return { id, title, ...(group ? { group } : {}), ...(keywords ? { keywords } : {}), ...(shortcut ? { shortcut } : {}) }
}

export function badgeSpec(args: Record<string, unknown>): { pageId: string; badge: BadgeContribution | null } {
  const pageId = pattern(args.pageId, PAGE_ID_PATTERN, 'pageId', 'a registered page id')
  if (args.badge === null || args.badge === undefined) return { pageId, badge: null }
  if (!isRecord(args.badge)) throw new CallError('invalid_argument', 'badge must be { text, tone? } or null.')
  const text = string(args.badge.text, 'badge.text', RUNTIME_LIMITS.badgeText)
  if (/[\r\n]/u.test(text)) throw new CallError('invalid_argument', 'badge.text must be one line.')
  const tone = args.badge.tone === undefined ? undefined : tone_(args.badge.tone, 'badge.tone')
  return { pageId, badge: { text, ...(tone ? { tone } : {}) } }
}

export function confirmSpec(args: Record<string, unknown>): ConfirmSpec {
  const tone = args.tone === undefined ? 'neutral' : args.tone
  if (tone !== 'neutral' && tone !== 'danger') throw new CallError('invalid_argument', 'tone must be neutral or danger.')
  return {
    title: string(args.title, 'title', RUNTIME_LIMITS.notifyTitle),
    body: args.body === undefined ? '' : string(args.body, 'body', RUNTIME_LIMITS.notifyBody, true),
    confirmText: args.confirmText === undefined ? 'Continue' : string(args.confirmText, 'confirmText', RUNTIME_LIMITS.confirmText),
    tone,
  }
}

export function clipboardText(args: Record<string, unknown>): string {
  if (typeof args.text !== 'string') throw new CallError('invalid_argument', 'text must be a string.')
  if (runes(args.text) > RUNTIME_LIMITS.clipboardText) throw new CallError('too_large', `Clipboard text is at most ${RUNTIME_LIMITS.clipboardText} characters.`)
  return args.text
}

/** One storage.setMany batch: values to write, `null` to delete. */
export function storageBatch(args: Record<string, unknown>): Record<string, string | null> {
  if (!isRecord(args.values)) throw new CallError('invalid_argument', 'values must be an object of keys to strings or null.')
  const entries = Object.entries(args.values)
  if (entries.length === 0 || entries.length > RUNTIME_LIMITS.storageBatch) throw new CallError('invalid_argument', `values must hold 1 to ${RUNTIME_LIMITS.storageBatch} keys.`)
  const batch: Record<string, string | null> = {}
  for (const [key, value] of entries) {
    storageKey({ key })
    if (value !== null && typeof value !== 'string') throw new CallError('invalid_argument', `values["${key}"] must be a string or null; use JSON.stringify for anything else.`)
    if (typeof value === 'string' && encoder.encode(value).length > STORAGE_VALUE_BYTES) throw new CallError('too_large', `values["${key}"] is over ${STORAGE_VALUE_BYTES / 1024} KiB.`)
    batch[key] = value
  }
  return batch
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

/** A key the plugin may use in its own storage. `setting:` keys hold the
 * operator's settings: the plugin reads them through `settings`, never here,
 * so it cannot overwrite what the operator chose. */
export function storageKey(args: Record<string, unknown>): string {
  const key = pattern(args.key, STORAGE_KEY_PATTERN, 'key', '1–64 letters, digits, dots, colons, dashes or underscores')
  if (key.startsWith(SETTING_PREFIX)) throw new CallError('invalid_argument', `Keys starting with "${SETTING_PREFIX}" hold the operator's settings; read them with switchboard.settings.`)
  return key
}

export function localId(args: Record<string, unknown>, field = 'id', shape: RegExp = LOCAL_ID_PATTERN): string {
  return pattern(args[field], shape, field, 'a valid id')
}

export function requireString(args: Record<string, unknown>, field: string, limit: number): string {
  return string(args[field], field, limit)
}

export function pattern(value: unknown, shape: RegExp, field: string, description: string): string {
  if (typeof value !== 'string' || !shape.test(value)) throw new CallError('invalid_argument', `${field} must be ${description}.`)
  return value
}

export function string(value: unknown, field: string, limit: number, allowEmpty = false): string {
  if (typeof value !== 'string' || (!allowEmpty && value.trim().length === 0)) throw new CallError('invalid_argument', `${field} must be a non-empty string.`)
  if (runes(value) > limit) throw new CallError('invalid_argument', `${field} is longer than ${limit} characters.`)
  return value
}

export const encoder = new TextEncoder()

/** Length in code points, so an emoji counts once, as the control plane
 * counts it. */
export function runes(value: string): number {
  return [...value].length
}

export function tone_(value: unknown, field: string): ContributionTone {
  if (!TONES.includes(value as ContributionTone)) throw new CallError('invalid_argument', `${field} must be one of ${TONES.join(', ')}.`)
  return value as ContributionTone
}

export function clip(value: string, limit: number): string {
  if (value.length <= limit) return value
  // Never cut a surrogate pair in half.
  return `${Array.from(value.slice(0, limit)).slice(0, limit - 1).join('').replace(/[\uD800-\uDBFF]$/u, '')}…`
}

export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
