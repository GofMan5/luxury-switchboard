// The 1.0.66 worker APIs for `pnpm plugin run`, headless.
//
// `shim` runs inside the plugin's context, right before the plugin script,
// and extends the prelude's `switchboard` with the new namespaces. Every call
// becomes one synchronous `bridge(op, json)` into `createHeadless`, which
// checks grants and limits the way the app does, records what happened and
// answers with stand-ins: net.fetch is `not_allowed` unless `--fetch` mocks
// the URL, db lives in memory, canvas draws are validated and counted, the bus
// is local (no self-delivery), dialogs answer `--dialog`, and relay hooks see
// one fictional request. Nothing here reaches the network, a disk or a relay.
import { optional } from './domain.mjs'

/** Runs in the plugin's realm; it must not close over this module. */
export function shim(real, bridge) {
  'use strict'
  const freeze = Object.freeze
  const send = (op, args) => {
    const reply = JSON.parse(bridge(op, JSON.stringify(args === undefined ? null : args)))
    if (reply.ok) return reply.value
    const error = new Error(reply.message)
    error.code = reply.code
    throw error
  }
  const later = (op, args) => { try { return Promise.resolve(send(op, args)) } catch (error) { return Promise.reject(error) } }
  const handler = (value) => { if (typeof value !== 'function') throw new TypeError('handler must be a function'); return value }
  const listen = (set, value) => { const entry = handler(value); set.add(entry); return function () { set.delete(entry) } }

  const frames = new Set()
  const inputs = new Set()
  const themes = new Set()
  const events = new Set()
  const channels = new Map()
  const hooks = new Map()

  globalThis.__cliHooks = freeze({
    frame: (tick) => { for (const entry of Array.from(frames)) entry(tick.dt, tick.t) },
    input: (event) => { for (const entry of Array.from(inputs)) entry(freeze(event)) },
    event: (event) => {
      const detail = freeze(Object.assign({}, event, { form: freeze(Object.assign({}, event.form)) }))
      for (const entry of Array.from(events)) if (!entry.pageId || entry.pageId === event.pageId) entry.handler(detail)
    },
    hook: (call) => {
      const hook = hooks.get(call.id)
      if (!hook) return
      Promise.resolve()
        .then(() => (call.response ? hook(freeze(call.request), freeze(call.response)) : hook(freeze(call.request))))
        .then((value) => send('hook.result', { id: call.id, value: value === undefined ? null : value }),
          (error) => send('hook.result', { id: call.id, error: String(error && error.message || error) }))
    },
  })

  const ui = freeze(Object.assign({}, real.ui, {
    onEvent: function (pageIdOrHandler, maybeHandler) {
      const off = real.ui.onEvent(pageIdOrHandler, maybeHandler)
      const entry = typeof pageIdOrHandler === 'function' ? { pageId: '', handler: pageIdOrHandler } : { pageId: String(pageIdOrHandler), handler: maybeHandler }
      events.add(entry)
      return function () { off(); events.delete(entry) }
    },
    render: (surface, tree) => (String(surface).indexOf(':') === -1 ? real.ui.render(surface, tree) : later('render', { surface: surface, tree: tree })),
    registerWidget: (spec) => later('slot', { kind: 'widget', spec: spec }),
    registerInspectorTab: (spec) => later('slot', { kind: 'inspector', spec: spec }),
    registerSettingsSection: (spec) => later('slot', { kind: 'settings', spec: spec }),
    registerAction: (spec) => later('slot', { kind: 'action', spec: spec }),
    registerHeaderButton: (spec) => later('slot', { kind: 'header', spec: spec }),
    unregister: (id) => later('unregister', { id: id }),
    setLayout: (layout) => later('layout', { layout: layout }),
    resetLayout: () => later('layout', { layout: null }),
    dialog: (spec) => later('dialog', spec).then((opened) => freeze({
      id: opened.id,
      update: (tree) => later('dialog.update', { id: opened.id, tree: tree }),
      close: () => later('dialog.close', { id: opened.id }),
      result: Promise.resolve(freeze({ action: opened.action, form: freeze({}) })),
    })),
    sound: freeze({ tone: (spec) => { send('tone', spec) } }),
    theme: () => later('theme'),
    onTheme: (value) => listen(themes, value),
    draw: (surface, canvas, ops) => { send('draw', { surface: surface, canvas: canvas, ops: ops }) },
    onFrame: (value) => listen(frames, value),
    onInput: (value) => listen(inputs, value),
  }))

  const collection = (name) => freeze({
    get: (id) => later('db', { name: name, op: 'get', id: id }),
    put: (id, doc) => later('db', { name: name, op: 'put', id: id, doc: doc }),
    delete: (id) => later('db', { name: name, op: 'delete', id: id }),
    list: (options) => later('db', { name: name, op: 'list', options: options || {} }),
    clear: () => later('db', { name: name, op: 'clear' }),
    stats: () => later('db', { name: name, op: 'stats' }),
  })
  const rule = (method, payload) => real.control.call('rules.' + method, payload)
  const hook = (phase) => function (filter, value) {
    const entry = handler(value)
    return later('hook', { phase: phase, filter: filter || {} }).then((added) => {
      hooks.set(added.id, entry)
      return function () { hooks.delete(added.id); return later('unhook', { id: added.id }) }
    })
  }

  return freeze(Object.assign({}, real, {
    ui: ui,
    settings: freeze(Object.assign({}, real.settings, { secrets: freeze(send('secrets')) })),
    net: freeze({
      fetch: (url, init) => later('fetch', { url: url, init: init || {} }).then((response) => freeze({
        status: response.status, ok: response.ok, headers: freeze(response.headers), text: response.text,
        json: () => JSON.parse(response.text),
      })),
    }),
    db: freeze({ collection: (name) => { send('db.name', { name: name }); return collection(name) } }),
    files: freeze({ open: (options) => later('files.open', options || {}), save: (file) => later('files.save', file) }),
    bus: freeze({
      publish: (channel, payload) => later('bus.publish', { channel: channel, payload: payload }),
      subscribe: (channel, value) => {
        send('bus.subscribe', { channel: channel })
        if (!channels.has(channel)) channels.set(channel, new Set())
        return listen(channels.get(channel), value)
      },
    }),
    rules: freeze({
      list: () => rule('list'),
      saveRequestRule: (value) => rule('saveRequestRule', { rule: value }),
      deleteRequestRule: (id) => rule('deleteRequestRule', { id: id }),
      saveRetryRule: (value) => rule('saveRetryRule', { rule: value }),
      deleteRetryRule: (id) => rule('deleteRetryRule', { id: id }),
      test: (input) => rule('test', input),
      stats: () => rule('stats'),
      recentErrors: (limit) => rule('recentErrors', limit === undefined ? {} : { limit: limit }),
    }),
    relay: freeze({ onRequest: hook('request'), onResponse: hook('response') }),
  }))
}

const NAME = /^[a-z0-9][a-z0-9_-]{0,47}$/u
const COLLECTION = /^[a-z0-9][a-z0-9_-]{0,31}$/u
const DOC_ID = /^[A-Za-z0-9._:-]{1,128}$/u
const CHANNEL = /^[a-z0-9][a-z0-9._:-]{0,63}$/u
const HOOK_ENDPOINTS = new Set(['responses', 'chat', 'messages', 'completions', 'embeddings', 'images', 'models', 'other'])
const SLOTS = {
  widget: { limit: 4, permission: 'ui:slots', surface: 'widget' },
  inspector: { limit: 4, permission: 'ui:slots', surface: 'inspector' },
  settings: { limit: 2, permission: 'ui:slots', surface: 'settings' },
  action: { limit: 8, permission: 'ui:slots' },
  header: { limit: 2, permission: 'ui:slots' },
}
const BUILT_IN_ROUTES = new Set(['overview', 'activity', 'insights', 'providers', 'keys', 'routes', 'rules', 'tests', 'guardrails', 'tunnel', 'clients', 'appearance', 'plugins', 'settings'])
const THEME = { mode: 'dark', tokens: { background: '#141414', surface: '#1c1c1c', text: '#e8e8e8', muted: '#9a9a9a', border: '#2e2e2e', accent: '#8ab4f8', success: '#6cbf6a', warning: '#e2c044', danger: '#dc6a5f', info: '#5a9fd6' } }
const GESTURE_MS = 10_000
const byteLength = (value) => Buffer.byteLength(JSON.stringify(value ?? null), 'utf8')
const refuse = (code, message) => Object.assign(new Error(message), { code })

/** One fictional chat request for relay hooks: no real prompt, model or provider. */
export const SAMPLE_REQUEST = {
  activityId: 'act-cli-0001', model: 'example-model', endpoint: 'chat', method: 'POST', path: '/v1/chat/completions',
  body: JSON.stringify({ model: 'example-model', messages: [{ role: 'user', content: 'Summarise ticket TCK-1042 for jane.doe@example.com, phone +1 555 0100.' }] }),
}
SAMPLE_REQUEST.bytes = Buffer.byteLength(SAMPLE_REQUEST.body, 'utf8')
const SAMPLE_RESPONSE = { status: 200, latencyMs: 840, tokens: { input: 31, output: 12, total: 43 }, body: JSON.stringify({ choices: [{ message: { role: 'assistant', content: 'Ticket TCK-1042: the customer asks for a refund.' }, finish_reason: 'stop' }] }) }

function globMatch(pattern, value) {
  const source = pattern.toLowerCase().replace(/[.+?^${}()|[\]\\]/gu, '\\$&').replace(/\*/gu, '.*')
  return new RegExp(`^${source}$`, 'u').test(value.toLowerCase())
}

function hostAllowed(hosts, host) {
  return hosts.some((entry) => (entry.startsWith('*.') ? host.endsWith(entry.slice(1)) : host === entry))
}

/**
 * The node side of `shim`. `grants` are the effective grants; `options`
 * carries `fetch` mocks (`prefix=body`), `dialog` (the action a dialog
 * answers) and the names of secrets the run filled in.
 */
export async function createHeadless(pkg, grants, options = {}) {
  const [canvas, tree] = await Promise.all([optional('features/plugins/domain/canvas.ts'), optional('features/plugins/domain/ui-tree.ts')])
  const checkDraw = canvas.module?.validateDrawOps ?? (() => ({ ok: true }))
  const checkTree = tree.module?.checkTree ?? (() => ({ ok: true }))
  const has = (permission) => grants.includes(permission)
  const need = (permission) => { if (!has(permission)) throw refuse('permission_denied', `This needs the "${permission}" permission.`) }
  const mocks = (options.fetch ?? []).map((pair) => {
    const at = pair.indexOf('=')
    return at > 0 ? { prefix: pair.slice(0, at), body: pair.slice(at + 1) } : { prefix: pair, body: '' }
  })
  const hosts = pkg.network?.hosts ?? []
  const secrets = options.secrets ?? []
  const collections = new Map()
  const hookResults = new Map()
  let nextHook = 1
  let nextDialog = 1
  let gestureAt = 0
  let toneSecond = 0
  let tonesThisSecond = 0

  const record = {
    draws: {}, drawProblems: [], frames: 0, tones: 0, tonesDropped: 0,
    slots: [], surfaces: {}, layout: [], dialogs: [], fetches: [], bus: [], hooks: [], hookDecisions: [], files: [],
  }

  const gesture = () => { if (Date.now() - gestureAt > GESTURE_MS) throw refuse('needs_gesture', 'This needs a click or key press in the last 10 seconds.') }
  const named = (value, what) => { if (typeof value !== 'string' || !NAME.test(value)) throw refuse('invalid_argument', `${what} must match ${NAME}.`); return value }
  const store = (name) => {
    if (typeof name !== 'string' || !COLLECTION.test(name)) throw refuse('invalid_argument', `A collection name matches ${COLLECTION}.`)
    if (!collections.has(name)) {
      if (collections.size >= 16) throw refuse('limit', 'At most 16 collections.')
      collections.set(name, new Map())
    }
    return collections.get(name)
  }
  const docId = (id) => { if (typeof id !== 'string' || !DOC_ID.test(id)) throw refuse('invalid_argument', `A document id matches ${DOC_ID}.`); return id }
  const totalBytes = () => [...collections.values()].reduce((sum, docs) => sum + [...docs.values()].reduce((inner, doc) => inner + byteLength(doc), 0), 0)
  const surfaceTree = (surface, value) => {
    const checked = checkTree(value)
    if (!checked.ok) throw refuse('invalid_tree', checked.problem)
    return value
  }

  const ops = {
    secrets: () => secrets,
    render({ surface, tree: value }) {
      if (!record.surfaces[surface]) throw refuse('not_found', `No surface "${surface}" is registered.`)
      record.surfaces[surface].tree = surfaceTree(surface, value)
      record.surfaces[surface].renders += 1
    },
    slot({ kind, spec }) {
      const slot = SLOTS[kind]
      need(slot.permission)
      const id = named(spec?.id, 'id')
      if (typeof spec.title !== 'string' || spec.title.trim() === '' || spec.title.length > 48) throw refuse('invalid_argument', 'title is 1–48 characters.')
      if (kind === 'inspector' && !['activity', 'provider'].includes(spec.scope)) throw refuse('invalid_argument', 'scope is "activity" or "provider".')
      if (kind === 'action' && !['activity', 'provider', 'key'].includes(spec.scope)) throw refuse('invalid_argument', 'scope is "activity", "provider" or "key".')
      if (kind === 'header' && typeof spec.icon !== 'string') throw refuse('invalid_argument', 'A header button needs an icon.')
      const existing = record.slots.findIndex((entry) => entry.kind === kind && entry.id === id)
      if (existing < 0 && record.slots.filter((entry) => entry.kind === kind).length >= slot.limit) throw refuse('limit', `At most ${slot.limit} of kind "${kind}".`)
      const entry = { kind, id, title: spec.title, ...(spec.scope ? { scope: spec.scope } : {}), ...(slot.surface ? { surface: `${slot.surface}:${id}` } : {}) }
      if (existing < 0) record.slots.push(entry)
      else record.slots[existing] = entry
      if (!slot.surface) return null
      record.surfaces[entry.surface] ??= { renders: 0, tree: null }
      return { surface: entry.surface }
    },
    unregister({ id }) {
      const index = record.slots.findIndex((entry) => entry.surface === id || entry.id === id)
      if (index < 0) return // unknown ids do nothing, as in the app
      const [removed] = record.slots.splice(index, 1)
      if (removed.surface) delete record.surfaces[removed.surface]
    },
    layout({ layout }) {
      need('ui:layout')
      if (layout === null) { record.layout.push({ reset: true }); return }
      const hidden = (layout?.hidden ?? []).filter((route) => BUILT_IN_ROUTES.has(route) && route !== 'plugins' && route !== 'settings')
      const order = (layout?.order ?? []).filter((route) => BUILT_IN_ROUTES.has(route))
      record.layout.push({ hidden, order, ...(layout?.startPage ? { startPage: layout.startPage } : {}) })
    },
    dialog(spec) {
      need('ui:dialogs')
      gesture()
      if (record.dialogs.some((entry) => entry.open)) throw refuse('busy', 'One dialog at a time.')
      surfaceTree('dialog', spec?.tree)
      const actions = Array.isArray(spec?.actions) ? spec.actions.map((action) => named(action?.id, 'An action id')) : []
      const answer = options.dialog && actions.includes(options.dialog) ? options.dialog : null
      const id = `d${nextDialog++}`
      record.dialogs.push({ id, title: spec.title, actions, answer, open: false })
      return { id, action: answer }
    },
    'dialog.update': ({ tree: value }) => { surfaceTree('dialog', value) },
    'dialog.close': () => null,
    tone(spec) {
      need('ui:sound')
      const { frequency, durationMs } = spec ?? {}
      if (!(frequency >= 40 && frequency <= 4000) || !(durationMs >= 10 && durationMs <= 2000)) throw refuse('invalid_argument', 'A tone is 40–4000 Hz for 10–2000 ms.')
      const second = Math.floor(Date.now() / 1000)
      if (second !== toneSecond) { toneSecond = second; tonesThisSecond = 0 }
      if (++tonesThisSecond > 20) { record.tonesDropped += 1; return }
      record.tones += 1
    },
    theme: () => THEME,
    draw({ surface, canvas: canvasId, ops: list }) {
      const checked = checkDraw(list)
      const key = `${surface}/${canvasId}`
      if (!checked.ok) { if (record.drawProblems.length < 10) record.drawProblems.push(`${key}: ${checked.problem}`); return }
      record.draws[key] = { count: (record.draws[key]?.count ?? 0) + 1, ops: list.length }
    },
    fetch({ url, init }) {
      need('net:fetch')
      let parsed
      try { parsed = new URL(url) } catch { throw refuse('invalid_argument', 'Not a URL.') }
      const entry = { method: init.method ?? 'GET', url: `${parsed.origin}${parsed.pathname}` }
      record.fetches.push(entry)
      if (parsed.protocol !== 'https:' || (parsed.port && parsed.port !== '443') || !hostAllowed(hosts, parsed.hostname)) {
        entry.result = 'not_allowed'
        throw refuse('not_allowed', 'The URL is not https or its host is not in network.hosts.')
      }
      const headers = Object.values(init.headers ?? {})
      if (!['GET', 'POST', 'PUT', 'PATCH', 'DELETE'].includes(entry.method) || headers.length > 32 || headers.some((value) => Buffer.byteLength(String(value), 'utf8') > 8192)) {
        entry.result = 'invalid_plugin'
        throw refuse('invalid_plugin', 'Plugin fetch request is malformed')
      }
      const text = `${headers.join('\n')}\n${init.body ?? ''}`
      for (const [, key] of text.matchAll(/\{\{secret:([A-Za-z0-9_]+)\}\}/gu)) {
        if (!secrets.includes(key)) { entry.result = 'missing_secret'; throw refuse('missing_secret', `The secret "${key}" has no value.`) }
      }
      const mock = mocks.find((candidate) => url.startsWith(candidate.prefix))
      if (!mock) { entry.result = 'not_allowed (no --fetch mock)'; throw refuse('not_allowed', 'pnpm plugin run sends no requests; mock this URL with --fetch <url-prefix>=<body>.') }
      entry.result = 200
      return { status: 200, ok: true, headers: { 'content-type': 'application/json' }, text: mock.body }
    },
    'db.name': ({ name }) => { need('storage'); store(name) },
    db({ name, op, id, doc, options: listOptions }) {
      need('storage')
      const docs = store(name)
      switch (op) {
        case 'get': return docs.has(docId(id)) ? structuredClone(docs.get(id)) : null
        case 'put': {
          docId(id)
          if (doc === null || doc === undefined) throw refuse('invalid_argument', 'A document is JSON other than null.')
          if (byteLength(doc) > 65_536) throw refuse('too_large', 'A document is at most 64 KiB.')
          if (totalBytes() - (docs.has(id) ? byteLength(docs.get(id)) : 0) + byteLength(doc) > 2 * 1024 * 1024) throw refuse('plugin_storage_full', 'Collections hold at most 2 MiB.')
          docs.set(id, doc)
          return null
        }
        case 'delete': return docs.delete(docId(id))
        case 'list': {
          const limit = listOptions.limit ?? 50
          if (!Number.isInteger(limit) || limit < 1 || limit > 200) throw refuse('invalid_argument', 'limit is 1–200.')
          const ids = [...docs.keys()].sort().filter((key) => key.startsWith(listOptions.prefix ?? '') && (!listOptions.after || key > listOptions.after))
          const page = ids.slice(0, limit)
          return { items: page.map((key) => ({ id: key, doc: docs.get(key) })), ...(ids.length > limit ? { next: page.at(-1) } : {}) }
        }
        case 'clear': { const count = docs.size; docs.clear(); return count }
        case 'stats': return { docs: docs.size, bytes: [...docs.values()].reduce((sum, value) => sum + byteLength(value), 0) }
        default: throw refuse('invalid_argument', `Unknown collection call "${op}".`)
      }
    },
    'files.open': () => { need('files'); gesture(); record.files.push({ open: 'cancelled' }); return null },
    'files.save': (file) => {
      need('files')
      gesture()
      if (typeof file?.name !== 'string' || typeof file.text !== 'string') throw refuse('invalid_argument', 'save needs a name and text.')
      record.files.push({ save: file.name, bytes: Buffer.byteLength(file.text, 'utf8') })
      return true
    },
    'bus.publish': ({ channel, payload }) => {
      need('bus')
      if (typeof channel !== 'string' || !CHANNEL.test(channel)) throw refuse('invalid_argument', `A channel matches ${CHANNEL}.`)
      if (byteLength(payload) > 32_768) throw refuse('too_large', 'A bus payload is at most 32 KiB.')
      if (record.bus.length >= 120) throw refuse('rate_limited', 'At most 120 messages a minute.')
      record.bus.push({ channel, bytes: byteLength(payload) })
    },
    'bus.subscribe': ({ channel }) => {
      need('bus')
      if (typeof channel !== 'string' || !CHANNEL.test(channel)) throw refuse('invalid_argument', `A channel matches ${CHANNEL}.`)
    },
    hook({ phase, filter }) {
      need('relay:content')
      if (record.hooks.filter((entry) => entry.phase === phase).length >= 2) throw refuse('limit', 'At most 2 hooks per phase.')
      const timeoutMs = filter.timeoutMs ?? 2000
      if (phase === 'request' && !(Number.isInteger(timeoutMs) && timeoutMs >= 100 && timeoutMs <= 5000)) throw refuse('invalid_argument', 'timeoutMs is 100–5000.')
      if ((filter.endpoints ?? []).some((endpoint) => !HOOK_ENDPOINTS.has(endpoint))) throw refuse('invalid_argument', 'Unknown endpoint in the filter.')
      if ((filter.models ?? []).length > 16) throw refuse('invalid_argument', 'At most 16 model globs.')
      const id = `h${nextHook++}`
      record.hooks.push({ id, phase, models: filter.models ?? [], endpoints: filter.endpoints ?? [], timeoutMs })
      return { id }
    },
    unhook({ id }) { record.hooks = record.hooks.filter((entry) => entry.id !== id) },
    'hook.result': ({ id, value, error }) => { hookResults.get(id)?.({ value, error }) },
  }

  const bridge = (op, json) => {
    try {
      const handler = ops[op]
      if (!handler) throw refuse('failed', `Unknown call "${op}".`)
      const value = handler(JSON.parse(json))
      return JSON.stringify({ ok: true, value: value ?? null })
    } catch (error) {
      return JSON.stringify({ ok: false, code: error.code ?? 'failed', message: error.message })
    }
  }

  const matches = (hook) => (hook.models.length === 0 || hook.models.some((pattern) => globMatch(pattern, SAMPLE_REQUEST.model)))
    && (hook.endpoints.length === 0 || hook.endpoints.includes(SAMPLE_REQUEST.endpoint))

  /** Runs each matching hook once on the sample request, as the relay would. */
  async function runHooks(worker) {
    let body = SAMPLE_REQUEST.body
    for (const hook of record.hooks.filter((entry) => matches(entry))) {
      const request = { ...SAMPLE_REQUEST, body }
      const settled = new Promise((resolve) => {
        hookResults.set(hook.id, resolve)
        setTimeout(() => resolve({ error: 'timeout' }), hook.phase === 'request' ? hook.timeoutMs : 5000)
      })
      worker.invoke('hook', hook.phase === 'request' ? { id: hook.id, request } : { id: hook.id, request: { ...SAMPLE_REQUEST, body: undefined }, response: SAMPLE_RESPONSE })
      const { value, error } = await settled
      hookResults.delete(hook.id)
      const decision = { hook: hook.id, phase: hook.phase }
      if (error) decision.outcome = error === 'timeout' ? 'timed out (the request continues)' : `failed: ${error} (the request continues)`
      else if (hook.phase === 'response') decision.outcome = 'observed'
      else if (value?.action === 'rewrite') {
        let valid = true
        if (value.body !== undefined) { try { JSON.parse(value.body) } catch { valid = false } }
        decision.outcome = valid ? 'rewrite' : 'rewrite with invalid JSON (ignored)'
        if (valid && value.body !== undefined) { body = value.body; decision.body = value.body }
        if (value.model) decision.model = value.model
        if (value.tags) decision.tags = value.tags
      } else if (value?.action === 'block') decision.outcome = `block ${value.block?.status}: ${value.block?.message}`
      else decision.outcome = value?.action === 'continue' ? 'continue' : 'no decision (treated as continue)'
      record.hookDecisions.push(decision)
    }
  }

  return {
    source: `switchboard = (${shim.toString()})(switchboard, __cliBridge);\n`,
    globals: { __cliBridge: bridge },
    record,
    gesture: () => { gestureAt = Date.now() },
    runHooks,
    notes: [canvas.error && `Canvas checks did not load (${canvas.error}).`, tree.error && `Tree checks did not load (${tree.error}).`].filter(Boolean),
  }
}
