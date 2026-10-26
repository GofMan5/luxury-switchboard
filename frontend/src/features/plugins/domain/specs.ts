import { CallError, PAGE_ID_PATTERN, encoder, isRecord, pattern, runes, string } from './protocol'

/**
 * Checks for the 1.0.66 runtime calls. Like the rest of the protocol, the
 * worker is untrusted: every argument is checked here before the host acts on
 * it, and the Go side checks again wherever a call reaches the sidecar.
 */

export const SLOT_LIMITS = { widgets: 4, inspectorTabs: 4, settingsSections: 2, actions: 8, headerButtons: 2, title: 48, description: 200 } as const

/** The curated icon set plugins may name; the shell maps each to a glyph. */
export const PLUGIN_ICON_NAMES = ['activity', 'chart', 'bell', 'box', 'clock', 'cpu', 'database', 'gauge', 'globe', 'layers', 'dashboard', 'list', 'puzzle', 'shield', 'terminal', 'zap'] as const
export type PluginIconName = (typeof PLUGIN_ICON_NAMES)[number]

/** Every built-in route a layout may name. */
export const BUILT_IN_ROUTES = ['overview', 'activity', 'insights', 'providers', 'keys', 'routes', 'rules', 'tests', 'guardrails', 'tunnel', 'clients', 'appearance', 'plugins', 'settings'] as const
/** Always on the rail, so the way back is never hidden. */
const NEVER_HIDDEN = new Set(['plugins', 'settings'])

export const NAME_PATTERN = /^[A-Za-z0-9_.:-]{1,64}$/u
export const COLLECTION_PATTERN = /^[a-z0-9][a-z0-9_-]{0,31}$/u
export const DOC_ID_PATTERN = /^[A-Za-z0-9._:-]{1,128}$/u
export const CHANNEL_PATTERN = /^[a-z0-9][a-z0-9._:-]{0,63}$/u
export const HOOK_ID_PATTERN = /^[a-z0-9][a-z0-9_-]{0,31}$/u

export const DATA_LIMITS = {
  docBytes: 64 * 1024,
  /** Collection writes (put, delete, clear) a plugin may make per minute. */
  writesPerMinute: 300,
  listLimit: { min: 1, max: 200, default: 50 },
  fetchBody: 64 * 1024,
  fetchHeaders: 32,
  fetchHeaderBytes: 8 * 1024,
  fetchTimeoutMs: { min: 1000, max: 30_000, default: 15_000 },
  fetchInflight: 4,
  fetchPerMinute: 60,
  fetchUrl: 2048,
  openBytes: 2 * 1024 * 1024,
  saveBytes: 5 * 1024 * 1024,
  fileName: 128,
  accept: 16,
  busPayload: 32 * 1024,
  busPerMinute: 120,
  busSubscriptions: 32,
  tonesPerSecond: 20,
  /** A tone spec is four numbers; anything past this is junk the worker
   * refuses before posting. */
  toneBytes: 1024,
  toneVolume: 0.3,
  dialogTitle: 120,
  dialogActions: 4,
  dialogLabel: 24,
  hookBody: 192 * 1024,
  hookModels: 16,
  hookModelRunes: 200,
  hookTimeoutMs: { min: 100, max: 5000, default: 2000 },
  hookTags: 8,
  blockMessage: 200,
} as const

export interface WidgetSpec { readonly id: string; readonly title: string; readonly order?: number; readonly wide?: boolean }
export interface InspectorTabSpec { readonly id: string; readonly title: string; readonly scope: 'activity' | 'provider' }
export interface SettingsSectionSpec { readonly id: string; readonly title: string; readonly description?: string }
export interface ActionSpec { readonly id: string; readonly title: string; readonly scope: 'activity' | 'provider' | 'key' | 'header'; readonly tone?: 'neutral' | 'danger'; readonly icon?: PluginIconName }
export interface LayoutSpec { readonly hidden: readonly string[]; readonly order: readonly string[]; readonly startPage?: string }
export interface DialogActionSpec { readonly id: string; readonly label: string; readonly tone: 'primary' | 'neutral' | 'danger' }
export interface DialogSpec { readonly title: string; readonly tree: unknown; readonly actions: readonly DialogActionSpec[]; readonly size: 'small' | 'medium' | 'large' }
export interface ToneSpec { readonly frequency: number; readonly durationMs: number; readonly type: OscillatorType; readonly volume: number }
export interface FetchSpec { readonly url: string; readonly method: string; readonly headers: Readonly<Record<string, string>>; readonly body?: string; readonly timeoutMs: number }
export interface ListSpec { readonly prefix?: string; readonly after?: string; readonly limit: number }
export interface OpenSpec { readonly accept: readonly string[]; readonly maxBytes: number }
export interface SaveSpec { readonly name: string; readonly text: string; readonly mime: string }
export interface HookSpec { readonly hookId: string; readonly phase: 'request' | 'response'; readonly models: readonly string[]; readonly endpoints: readonly string[]; readonly timeoutMs: number }
export type HookDecision =
  | { readonly action: 'continue' }
  | { readonly action: 'rewrite'; readonly body?: string; readonly model?: string; readonly tags?: readonly string[] }
  | { readonly action: 'block'; readonly block: { readonly status: number; readonly message: string }; readonly tags?: readonly string[] }

const surfaceId = (args: Record<string, unknown>) => pattern(args.id, PAGE_ID_PATTERN, 'id', 'lowercase letters, digits, dashes and underscores, up to 48')
const title = (args: Record<string, unknown>) => string(args.title, 'title', SLOT_LIMITS.title)

function oneOf<T extends string>(value: unknown, field: string, allowed: readonly T[]): T {
  if (!allowed.includes(value as T)) throw new CallError('invalid_argument', `${field} must be one of ${allowed.join(', ')}.`)
  return value as T
}

function optionalIcon(value: unknown): PluginIconName | undefined {
  return value === undefined ? undefined : oneOf(value, 'icon', PLUGIN_ICON_NAMES)
}

function inRange(value: unknown, field: string, min: number, max: number, integer = true): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || (integer && !Number.isInteger(value)) || value < min || value > max) {
    throw new CallError('invalid_argument', `${field} must be ${integer ? 'a whole number' : 'a number'} from ${min} to ${max}.`)
  }
  return value
}

function bytes(value: string): number {
  return encoder.encode(value).length
}

export function widgetSpec(args: Record<string, unknown>): WidgetSpec {
  const order = args.order === undefined ? undefined : inRange(args.order, 'order', -1000, 1000)
  if (args.wide !== undefined && typeof args.wide !== 'boolean') throw new CallError('invalid_argument', 'wide must be a boolean.')
  return { id: surfaceId(args), title: title(args), ...(order !== undefined ? { order } : {}), ...(args.wide ? { wide: true } : {}) }
}

export function inspectorTabSpec(args: Record<string, unknown>): InspectorTabSpec {
  return { id: surfaceId(args), title: title(args), scope: oneOf(args.scope, 'scope', ['activity', 'provider'] as const) }
}

export function settingsSectionSpec(args: Record<string, unknown>): SettingsSectionSpec {
  const description = args.description === undefined ? undefined : string(args.description, 'description', SLOT_LIMITS.description, true)
  return { id: surfaceId(args), title: title(args), ...(description ? { description } : {}) }
}

export function actionSpec(args: Record<string, unknown>): ActionSpec {
  const tone = args.tone === undefined ? undefined : oneOf(args.tone, 'tone', ['neutral', 'danger'] as const)
  const icon = optionalIcon(args.icon)
  return { id: surfaceId(args), title: title(args), scope: oneOf(args.scope, 'scope', ['activity', 'provider', 'key'] as const), ...(tone ? { tone } : {}), ...(icon ? { icon } : {}) }
}

export function headerButtonSpec(args: Record<string, unknown>): ActionSpec {
  if (args.icon === undefined) throw new CallError('invalid_argument', 'icon is required: header buttons show only their icon.')
  return { id: surfaceId(args), title: title(args), scope: 'header', icon: optionalIcon(args.icon) }
}

/** A route as the shell names it: `#/providers`, `/providers` and
 * `providers` are the same page. */
export function normalizeRoute(route: string): string {
  return route.replace(/^#?\/?/u, '')
}

/** A layout over built-in routes. Unknown ids are ignored, as the shell
 * ignores them; `plugins` and `settings` are never hidden. */
export function layoutSpec(args: Record<string, unknown>, isRoute: (route: string) => boolean): LayoutSpec {
  const ids = (value: unknown, field: string): string[] => {
    if (value === undefined) return []
    if (!Array.isArray(value) || value.length > BUILT_IN_ROUTES.length * 2 || value.some((item) => typeof item !== 'string')) {
      throw new CallError('invalid_argument', `${field} must be a list of built-in page ids.`)
    }
    return [...new Set((value as string[]).filter((id) => (BUILT_IN_ROUTES as readonly string[]).includes(id)))]
  }
  const hidden = ids(args.hidden, 'hidden').filter((id) => !NEVER_HIDDEN.has(id))
  const order = ids(args.order, 'order')
  let startPage: string | undefined
  if (args.startPage !== undefined) {
    if (typeof args.startPage !== 'string' || args.startPage.length > 160) throw new CallError('invalid_argument', 'startPage must be a route.')
    startPage = normalizeRoute(args.startPage)
    if (!isRoute(startPage)) throw new CallError('not_found', 'startPage is not a page of this window.')
  }
  return { hidden, order, ...(startPage ? { startPage } : {}) }
}

export function dialogSpec(args: Record<string, unknown>): DialogSpec {
  const dialogTitle = string(args.title, 'title', DATA_LIMITS.dialogTitle)
  if (!Array.isArray(args.actions) || args.actions.length > DATA_LIMITS.dialogActions) throw new CallError('invalid_argument', `actions must be a list of at most ${DATA_LIMITS.dialogActions} buttons.`)
  const seen = new Set<string>()
  const actions = args.actions.map((raw, index): DialogActionSpec => {
    if (!isRecord(raw)) throw new CallError('invalid_argument', `actions[${index}] must be { id, label, tone? }.`)
    const id = pattern(raw.id, NAME_PATTERN, `actions[${index}].id`, '1–64 letters, digits, dots, colons, dashes or underscores')
    if (seen.has(id)) throw new CallError('invalid_argument', `actions[${index}].id repeats "${id}".`)
    seen.add(id)
    return {
      id,
      label: string(raw.label, `actions[${index}].label`, DATA_LIMITS.dialogLabel),
      tone: raw.tone === undefined ? 'neutral' : oneOf(raw.tone, `actions[${index}].tone`, ['primary', 'neutral', 'danger'] as const),
    }
  })
  const size = args.size === undefined ? 'medium' : oneOf(args.size, 'size', ['small', 'medium', 'large'] as const)
  return { title: dialogTitle, tree: args.tree, actions, size }
}

/** A tone to play, or null to drop it silently: sound is fire-and-forget,
 * so a bad spec is a warning, never a stopped plugin. */
export function toneSpec(raw: Record<string, unknown>): ToneSpec | string {
  try {
    const type = raw.type === undefined ? 'sine' : oneOf(raw.type, 'type', ['sine', 'square', 'triangle', 'sawtooth'] as const)
    const volume = raw.volume === undefined ? 1 : inRange(raw.volume, 'volume', 0, 1, false)
    return {
      frequency: inRange(raw.frequency, 'frequency', 40, 4000, false),
      durationMs: inRange(raw.durationMs, 'durationMs', 10, 2000, false),
      type,
      volume: volume * DATA_LIMITS.toneVolume,
    }
  } catch (error) {
    return error instanceof Error ? error.message : 'invalid tone'
  }
}

const METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE'] as const
const HEADER_NAME = /^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,256}$/u

export function fetchSpec(args: Record<string, unknown>): FetchSpec {
  if (typeof args.url !== 'string' || args.url.length > DATA_LIMITS.fetchUrl || !/^https:\/\//iu.test(args.url)) {
    throw new CallError('not_allowed', 'url must be an https:// URL of a host the package lists in network.hosts.')
  }
  const method = args.method === undefined ? 'GET' : oneOf(args.method, 'method', METHODS)
  const headers: Record<string, string> = {}
  if (args.headers !== undefined) {
    if (!isRecord(args.headers)) throw new CallError('invalid_argument', 'headers must be an object of names to strings.')
    const entries = Object.entries(args.headers)
    if (entries.length > DATA_LIMITS.fetchHeaders) throw new CallError('invalid_argument', `headers may hold at most ${DATA_LIMITS.fetchHeaders} entries.`)
    for (const [name, value] of entries) {
      if (!HEADER_NAME.test(name)) throw new CallError('invalid_argument', 'Header names must be HTTP tokens.')
      // eslint-disable-next-line no-control-regex -- control characters are exactly what this refuses
      if (typeof value !== 'string' || /[\r\n\0]/u.test(value)) throw new CallError('invalid_argument', `headers["${name}"] must be a one-line string.`)
      if (bytes(name) + bytes(value) > DATA_LIMITS.fetchHeaderBytes) throw new CallError('too_large', `headers["${name}"] is over ${DATA_LIMITS.fetchHeaderBytes / 1024} KiB.`)
      headers[name] = value
    }
  }
  let body: string | undefined
  if (args.body !== undefined) {
    if (typeof args.body !== 'string') throw new CallError('invalid_argument', 'body must be a string; use JSON.stringify for anything else.')
    if (bytes(args.body) > DATA_LIMITS.fetchBody) throw new CallError('too_large', `body is over ${DATA_LIMITS.fetchBody / 1024} KiB.`)
    body = args.body
  }
  const { min, max, default: fallback } = DATA_LIMITS.fetchTimeoutMs
  const timeoutMs = args.timeoutMs === undefined ? fallback : inRange(args.timeoutMs, 'timeoutMs', min, max)
  return { url: args.url, method, headers, ...(body !== undefined ? { body } : {}), timeoutMs }
}

export function collectionName(args: Record<string, unknown>): string {
  return pattern(args.collection, COLLECTION_PATTERN, 'collection', 'a collection name: lowercase letters, digits, dashes and underscores, up to 32')
}

export function docId(args: Record<string, unknown>, field = 'docId'): string {
  return pattern(args[field], DOC_ID_PATTERN, field, '1–128 letters, digits, dots, colons, dashes or underscores')
}

/** A document to store: any JSON value but null, at most 64 KiB. Returns the
 * value itself; the worker already sent it as structured-cloned data. */
export function docValue(value: unknown): unknown {
  if (value === undefined || value === null) throw new CallError('invalid_argument', 'doc must be a JSON value; use delete to remove a document.')
  let json: string | undefined
  try {
    json = JSON.stringify(value)
  } catch {
    json = undefined
  }
  if (json === undefined) throw new CallError('invalid_argument', 'doc must be a JSON value.')
  if (bytes(json) > DATA_LIMITS.docBytes) throw new CallError('too_large', `A document is at most ${DATA_LIMITS.docBytes / 1024} KiB.`)
  return value
}

export function listSpec(args: Record<string, unknown>): ListSpec {
  const { min, max, default: fallback } = DATA_LIMITS.listLimit
  const limit = args.limit === undefined ? fallback : inRange(args.limit, 'limit', min, max)
  let prefix: string | undefined
  if (args.prefix !== undefined && args.prefix !== '') {
    if (typeof args.prefix !== 'string' || !/^[A-Za-z0-9._:-]{1,128}$/u.test(args.prefix)) throw new CallError('invalid_argument', 'prefix must be part of a document id.')
    prefix = args.prefix
  }
  const after = args.after === undefined || args.after === '' ? undefined : docId(args, 'after')
  return { ...(prefix ? { prefix } : {}), ...(after ? { after } : {}), limit }
}

export function openSpec(args: Record<string, unknown>): OpenSpec {
  let accept: string[] = []
  if (args.accept !== undefined) {
    if (!Array.isArray(args.accept) || args.accept.length > DATA_LIMITS.accept || args.accept.some((item) => typeof item !== 'string' || !/^\.[A-Za-z0-9]{1,16}$/u.test(item))) {
      throw new CallError('invalid_argument', `accept must list at most ${DATA_LIMITS.accept} extensions such as ".json".`)
    }
    accept = args.accept as string[]
  }
  const maxBytes = args.maxBytes === undefined ? DATA_LIMITS.openBytes : inRange(args.maxBytes, 'maxBytes', 1, DATA_LIMITS.openBytes)
  return { accept, maxBytes }
}

export function saveSpec(args: Record<string, unknown>): SaveSpec {
  const name = string(args.name, 'name', DATA_LIMITS.fileName)
  // A file name, never a path: the operator chooses where it goes.
  // eslint-disable-next-line no-control-regex -- control characters are exactly what this refuses
  if (/[\\/:*?"<>|\0-\x1f]/u.test(name) || /^\.+$/u.test(name)) throw new CallError('invalid_argument', 'name must be a file name without a path.')
  if (typeof args.text !== 'string') throw new CallError('invalid_argument', 'text must be a string.')
  if (bytes(args.text) > DATA_LIMITS.saveBytes) throw new CallError('too_large', `A saved file is at most ${DATA_LIMITS.saveBytes / 1024 / 1024} MiB.`)
  let mime = 'text/plain'
  if (args.mime !== undefined) {
    if (typeof args.mime !== 'string' || !/^[a-z]+\/[a-z0-9.+-]{1,64}$/u.test(args.mime)) throw new CallError('invalid_argument', 'mime must be a type such as "application/json".')
    mime = args.mime
  }
  return { name, text: args.text, mime }
}

export function channel(args: Record<string, unknown>): string {
  return pattern(args.channel, CHANNEL_PATTERN, 'channel', 'lowercase letters, digits, dots, colons, dashes and underscores, up to 64')
}

/** A bus payload: JSON of at most 32 KiB, delivered as a fresh parse so no
 * subscriber shares an object with the sender. */
export function busPayload(value: unknown): unknown {
  let json: string | undefined
  try {
    json = JSON.stringify(value ?? null)
  } catch {
    json = undefined
  }
  if (json === undefined) throw new CallError('invalid_argument', 'payload must be JSON.')
  if (bytes(json) > DATA_LIMITS.busPayload) throw new CallError('too_large', `A bus payload is at most ${DATA_LIMITS.busPayload / 1024} KiB.`)
  return JSON.parse(json) as unknown
}

const ENDPOINTS = ['responses', 'chat', 'messages', 'completions', 'embeddings', 'images', 'models', 'other'] as const

export function hookSpec(args: Record<string, unknown>): HookSpec {
  const hookId = pattern(args.hookId, HOOK_ID_PATTERN, 'hookId', 'a hook id')
  const phase = oneOf(args.phase, 'phase', ['request', 'response'] as const)
  const models = args.models === undefined ? [] : args.models
  if (!Array.isArray(models) || models.length > DATA_LIMITS.hookModels || models.some((glob) => typeof glob !== 'string' || glob.length === 0 || runes(glob) > DATA_LIMITS.hookModelRunes || /\p{Cc}/u.test(glob))) {
    throw new CallError('invalid_argument', `models must list at most ${DATA_LIMITS.hookModels} globs of 1–${DATA_LIMITS.hookModelRunes} characters.`)
  }
  const endpoints = args.endpoints === undefined ? [] : args.endpoints
  if (!Array.isArray(endpoints)) throw new CallError('invalid_argument', 'endpoints must be a list.')
  endpoints.forEach((endpoint, index) => oneOf(endpoint, `endpoints[${index}]`, ENDPOINTS))
  const { min, max, default: fallback } = DATA_LIMITS.hookTimeoutMs
  const timeoutMs = phase === 'response' || args.timeoutMs === undefined ? fallback : inRange(args.timeoutMs, 'timeoutMs', min, max)
  return { hookId, phase, models: models as string[], endpoints: [...new Set(endpoints as string[])], timeoutMs }
}

const TAG = /^[a-z0-9][a-z0-9_.-]{0,31}$/u
const BLOCK_STATUSES = [400, 403, 429, 503]

/** A request hook's answer, or a problem; a bad answer lets the request
 * continue (fail-open), as a timeout does. */
export function hookDecision(raw: unknown): HookDecision | string {
  if (!isRecord(raw)) return 'A request hook must return { action: "continue" | "rewrite" | "block" }.'
  const tags = (): readonly string[] | undefined | string => {
    if (raw.tags === undefined) return undefined
    if (!Array.isArray(raw.tags) || raw.tags.length > DATA_LIMITS.hookTags || raw.tags.some((tag) => typeof tag !== 'string' || !TAG.test(tag))) {
      return `tags must list at most ${DATA_LIMITS.hookTags} lowercase tags.`
    }
    return raw.tags as string[]
  }
  switch (raw.action) {
    case 'continue': return { action: 'continue' }
    case 'rewrite': {
      const list = tags()
      if (typeof list === 'string') return list
      if (raw.body !== undefined && (typeof raw.body !== 'string' || bytes(raw.body) > DATA_LIMITS.hookBody)) return `body must be a string of at most ${DATA_LIMITS.hookBody / 1024} KiB.`
      if (raw.model !== undefined && (typeof raw.model !== 'string' || raw.model.length === 0 || runes(raw.model) > DATA_LIMITS.hookModelRunes)) return 'model must be a model name.'
      return { action: 'rewrite', ...(raw.body !== undefined ? { body: raw.body as string } : {}), ...(raw.model !== undefined ? { model: raw.model as string } : {}), ...(list ? { tags: list } : {}) }
    }
    case 'block': {
      const list = tags()
      if (typeof list === 'string') return list
      if (!isRecord(raw.block) || !BLOCK_STATUSES.includes(raw.block.status as number)) return 'block.status must be 400, 403, 429 or 503.'
      const message = raw.block.message
      if (typeof message !== 'string' || message.trim().length === 0 || runes(message) > DATA_LIMITS.blockMessage) return `block.message must be 1–${DATA_LIMITS.blockMessage} characters.`
      return { action: 'block', block: { status: raw.block.status as number, message }, ...(list ? { tags: list } : {}) }
    }
    default:
      return 'action must be "continue", "rewrite" or "block".'
  }
}
