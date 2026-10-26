// `pnpm plugin run`: the app's own PluginHost driving the app's own worker
// prelude inside node:vm, against the dev fixture control plane. A quick way
// to see what a plugin registers and renders without opening the window.
//
// node:vm is NOT a sandbox: the plugin runs in this process. Trusted code only.
import { extensions, FRONTEND_SRC, host, manifest, optional, permissions, plugin, settings, workerSource } from './domain.mjs'
import { checkoutVersion, UsageError } from './package.mjs'
import { createHeadless } from './headless.mjs'
import { vmWorkerFactory } from './vm-worker.mjs'

const STORAGE = { keys: 64, valueBytes: 8192, totalBytes: 65536, key: /^[A-Za-z0-9._:-]{1,64}$/u }
const STABLE = new Set(['running', 'active', 'crashed', 'stopped'])
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const { PluginBus } = await import(new URL('features/plugins/application/plugin-bus.ts', FRONTEND_SRC).href)
/** The dark theme's colours, as `ui.theme()` reports them in the app. */
const THEME = {
  mode: 'dark',
  tokens: { background: '#141414', surface: '#1c1c1c', text: '#ededed', muted: '#9a9a9a', border: '#2e2e2e', accent: '#8ab4f8', success: '#5fb97a', warning: '#d9a43b', danger: '#e5645b', info: '#6aa6e8' },
}

/** Storage with the control plane's limits, kept in memory for one run. */
function memoryStorage(seed) {
  let values = { ...seed }
  const fail = (code, message) => Object.assign(new Error(message), { code })
  return {
    storageGet: async () => ({ ...values }),
    storageUpdate: async (_id, changes) => {
      const next = { ...values }
      for (const [key, value] of Object.entries(changes)) {
        if (!STORAGE.key.test(key)) throw fail('invalid_argument', `"${key}" is not a storage key.`)
        if (value === null) delete next[key]
        else if (manifest.byteLength(value) > STORAGE.valueBytes) throw fail('plugin_storage_full', 'A stored value is at most 8 KiB.')
        else next[key] = value
      }
      if (Object.keys(next).length > STORAGE.keys || manifest.byteLength(JSON.stringify(next)) > STORAGE.totalBytes) throw fail('plugin_storage_full', 'Plugin storage is full (64 keys, 64 KiB).')
      values = next
      return { ...values }
    },
    values: () => ({ ...values }),
  }
}

/** The fixture control plane is a browser module: give it the few globals it reads. */
async function fixtureControl(notes) {
  globalThis.window ??= {
    localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
    location: { search: '' },
    setTimeout, clearTimeout, setInterval, clearInterval,
  }
  const [fixture, port] = await Promise.all([optional('dev/fixture-session.ts'), optional('features/plugins/adapters/stdio-plugins-port.ts')])
  if (fixture.module && port.module) {
    const session = new fixture.module.FixtureSession()
    await session.start()
    return { control: new port.module.SessionControlPort(session), capabilities: session.capabilities, stop: () => session.stop() }
  }
  notes.push(`The fixture control plane did not load (${fixture.error || port.error}); control.call answers "unavailable".`)
  const unavailable = () => Promise.reject(Object.assign(new Error('No control plane in this run.'), { code: 'unavailable' }))
  return { control: { call: unavailable, subscribe: () => () => {} }, capabilities: ['plugins.manage'], stop: () => {} }
}

async function routeCheck(capabilities, notes) {
  const navigation = await optional('app/navigation.ts')
  if (!navigation.module) {
    notes.push(`The shell's route list did not load (${navigation.error}); navigate accepts plugin routes only.`)
    return (route) => /^plugin\//u.test(route)
  }
  const routes = new Set(navigation.module.visibleNavigation(capabilities).flatMap((section) => section.items.map((item) => item.id)))
  return (route) => routes.has(route) || navigation.module.isPluginRoute(route)
}

/** Values for the settings the run fills in. Secrets keep only their names: a value never reaches the report. */
function settingSeed(pkg, pairs) {
  const seed = {}
  const secrets = []
  const declared = new Set((pkg.settings ?? []).map((setting) => setting.key))
  const secret = new Set((pkg.settings ?? []).filter((setting) => setting.type === 'secret').map((setting) => setting.key))
  for (const pair of pairs) {
    const at = pair.indexOf('=')
    const key = at > 0 ? pair.slice(0, at) : ''
    if (!declared.has(key)) throw new UsageError(`--setting ${pair}: the package declares no setting "${key}".`)
    const raw = pair.slice(at + 1)
    if (secret.has(key)) { if (raw !== '') secrets.push(key); continue }
    let value
    try { value = JSON.parse(raw) } catch { value = raw }
    seed[settings.settingStorageKey(key)] = JSON.stringify(value)
  }
  return { seed, secrets }
}

/**
 * Stack frames as the author knows them: `plugin.js:<n>` (the prelude plus
 * the script) becomes a line of the script, and frames from the prelude, this
 * harness or Node are dropped.
 */
export function tidyStack(text, offset, scriptName = 'main.js') {
  if (!text.includes('\n')) return text
  return text.split('\n').flatMap((line) => {
    if (!/^\s+at /u.test(line)) return [line]
    const match = /plugin\.js:(\d+)(?::(\d+))?/u.exec(line)
    if (!match) return []
    const at = Number(match[1]) - offset
    if (at <= 0) return []
    return [line.replace(match[0], `${scriptName}:${at}${match[2] ? `:${match[2]}` : ''}`)]
  }).join('\n')
}

/** `page:action`, `widget:id:action` (a slot surface) or a bare action or header button id. */
function parseEvent(text) {
  if (!text.includes(':')) return { pageId: '', action: text }
  const at = text.lastIndexOf(':')
  if (at <= 0 || at === text.length - 1) throw new UsageError(`--event ${text}: use <pageId>:<action>, <surface>:<action> or <actionId>.`)
  return { pageId: text.slice(0, at), action: text.slice(at + 1) }
}

/** Canvas nodes in a tree, depth first. */
function canvases(tree, found = []) {
  if (!tree || typeof tree !== 'object') return found
  if (tree.type === 'canvas') found.push(tree)
  for (const child of Array.isArray(tree.children) ? tree.children : []) canvases(child, found)
  return found
}

const FRAMES = 5
const keyEvent = (surface, canvas, kind, key) => ({ surface, canvas, kind, key, code: key.length === 1 ? `Key${key.toUpperCase()}` : key, repeat: false, shift: false, alt: false, ctrl: false, meta: false })
const isSlotEvent = (record, event) => event.pageId === '' || Object.hasOwn(record.surfaces, event.pageId)
const slotFor = (record, event) => (event.pageId === ''
  ? record.slots.find((entry) => !entry.surface && entry.id === event.action)
  : record.slots.find((entry) => entry.surface === event.pageId))

/** The 1.0.66 surfaces: slot shows, canvases, keys, frames, slot and action clicks, relay hooks. */
async function drive(runner, worker, headless, events, keys, problems) {
  const { record } = headless
  for (const slot of record.slots.filter((entry) => entry.surface)) {
    worker.invoke('event', { kind: 'show', pageId: slot.surface, form: {}, ...(slot.kind === 'inspector' ? { context: { scope: slot.scope, id: 'req-fixture' } } : {}) })
  }
  await sleep(100)
  const surfaces = [
    ...[...runner.pages()].map((page) => ({ surface: page.pageId, tree: page.tree, page: true })),
    ...Object.entries(record.surfaces).map(([surface, entry]) => ({ surface, tree: entry.tree, page: false })),
  ].filter((entry) => canvases(entry.tree).length)
  for (const entry of surfaces) if (entry.page) runner.dispatch(entry.surface, { kind: 'show', form: {} })
  const keyed = surfaces.flatMap((entry) => canvases(entry.tree).filter((node) => node.keys).map((node) => ({ surface: entry.surface, canvas: node.id })))[0]
  if (keys.length && !keyed) problems.push('--key: no canvas on screen takes keys (keys: true).')
  for (const key of keyed ? keys : []) {
    headless.gesture()
    worker.invoke('input', keyEvent(keyed.surface, keyed.canvas, 'keydown', key))
    worker.invoke('input', keyEvent(keyed.surface, keyed.canvas, 'keyup', key))
    await sleep(30)
  }
  if (surfaces.some((entry) => canvases(entry.tree).some((node) => node.frames))) {
    for (let frame = 1; frame <= FRAMES; frame += 1) {
      worker.invoke('frame', { dt: 16, t: frame * 16 })
      record.frames += 1
      await sleep(16)
    }
  }
  for (const event of events.filter((entry) => isSlotEvent(record, entry))) {
    const slot = slotFor(record, event)
    if (!slot) continue
    headless.gesture()
    worker.invoke('event', event.pageId === ''
      ? { kind: 'action', pageId: '', action: event.action, form: {}, scope: slot.kind === 'header' ? 'header' : slot.scope, target: slot.kind === 'header' ? '' : 'req-fixture' }
      : { kind: 'action', pageId: event.pageId, action: event.action, form: {} })
    await sleep(150)
  }
  await headless.runHooks(worker)
}

async function play(runner, points, pluginId, events, commands, seconds, problems, headless, worker, keys) {
  runner.start()
  const settleBy = Date.now() + host.HOST_LIMITS.startMs + 500
  while (!STABLE.has(runner.runtime().state) && Date.now() < settleBy) await sleep(20)
  await sleep(150) // the activate handlers and their first renders
  for (const event of events) {
    if (isSlotEvent(headless.record, event)) {
      if (!slotFor(headless.record, event)) problems.push(`--event ${event.pageId ? `${event.pageId}:` : ''}${event.action}: the plugin registered no such ${event.pageId ? 'surface' : 'action or header button'}.`)
      continue
    }
    if (![...runner.pages()].some((page) => page.pageId === event.pageId)) {
      problems.push(`--event ${event.pageId}:${event.action}: the plugin registered no page "${event.pageId}".`)
      continue
    }
    headless.gesture()
    runner.dispatch(event.pageId, { kind: 'show', form: {} })
    await sleep(50)
    runner.dispatch(event.pageId, { kind: 'action', action: event.action, form: {} })
    await sleep(150)
  }
  for (const local of commands) {
    const id = `${plugin.pluginOwner(pluginId)}:${local}`
    const command = points.snapshot().commands.find((entry) => entry.id === id)
    if (!command) { problems.push(`--command ${local}: the plugin registered no such command.`); continue }
    headless.gesture()
    await command.run()
    await sleep(150)
  }
  if (worker()) await drive(runner, worker(), headless, events, keys, problems)
  await sleep(Math.max(0, seconds * 1000))
}

/**
 * Starts the plugin, lets it settle, plays the requested page events and
 * commands, waits `seconds`, then stops everything and reports what it did.
 * `pkg` is a checked package (validate first).
 */
export async function runPackage(pkg, options = {}) {
  const seconds = options.seconds ?? 3
  const appVersion = options.appVersion ?? checkoutVersion()
  const notes = []
  const problems = []
  const record = {
    notifications: [], navigations: [], refusedTrees: [], saved: [], sounds: [],
  }
  if (manifest.needsNewerApp(pkg.minAppVersion, appVersion)) {
    return { ok: false, problems: [`The package needs Luxury Switchboard ${pkg.minAppVersion} or newer; this run is ${appVersion} (pass --app-version).`], notes, state: 'not started' }
  }
  const requested = !options.grant || options.grant === 'all' ? pkg.permissions : options.grant.split(',').map((item) => item.trim()).filter(Boolean)
  for (const permission of requested) if (!pkg.permissions.includes(permission)) notes.push(`--grant ${permission}: the package does not declare it, so it is not granted.`)
  const grants = permissions.effectiveGrants(pkg.permissions, requested)
  const events = (options.events ?? []).map(parseEvent)
  const { seed, secrets } = settingSeed(pkg, options.settings ?? [])
  const storage = memoryStorage(seed)
  const headless = await createHeadless(pkg, grants, { fetch: options.fetch, dialog: options.dialog, confirm: options.confirm, secrets })
  notes.push(...headless.notes)

  const fixture = await fixtureControl(notes)
  const isRoute = await routeCheck(fixture.capabilities, notes)
  const points = new extensions.ExtensionPoints()
  let offset = 0
  let worker
  // The shim goes right after the prelude's own 'use strict', before line 1 of the script.
  const shimLines = headless.source.split('\n').length - 1
  const factory = vmWorkerFactory((script, context) => {
    const source = workerSource.workerSource(script, context)
    const head = source.text.split('\n').slice(0, source.offset).join('\n')
    offset = source.offset + shimLines
    return { text: `${head}\n${headless.source}${source.text.slice(head.length + 1)}`, offset }
  }, { globals: headless.globals })
  const workers = { create: (script, context) => (worker = factory.create(script, context)) }
  const tidy = (text) => tidyStack(text, offset, options.scriptName)
  const detail = plugin.normalizeDetail({
    plugin: { ...pkg, script: undefined, css: undefined, theme: undefined, settings: undefined, granted: grants, enabled: true, updatedAt: new Date().toISOString(), sizeBytes: manifest.packageBytes(pkg), hasScript: Boolean(pkg.script?.trim()), hasCss: Boolean(pkg.css), hasTheme: Boolean(pkg.theme), hasSettings: Boolean(pkg.settings?.length), hosts: pkg.network?.hosts ?? [] },
    script: pkg.script ?? '', css: pkg.css ?? '', theme: pkg.theme ?? null, settings: pkg.settings ?? [],
  })
  let runner
  const deps = {
    workers,
    control: fixture.control,
    storage,
    extensions: points,
    notify: (announcement) => { record.notifications.push(announcement) },
    // The shim answers these (headless.mjs), so the host never asks.
    confirm: async () => options.confirm !== 'no',
    clipboard: async () => {},
    navigate: (route) => { record.navigations.push(route) },
    isRoute,
    app: { version: appVersion, capabilities: fixture.capabilities },
    bus: new PluginBus(),
    // No window here: an open picks nothing, a save is recorded.
    files: {
      open: async () => null,
      save: async ({ name, text }) => { record.saved.push({ name, bytes: manifest.byteLength(text) }); return true },
    },
    sound: (tone) => { record.sounds.push(tone) },
    theme: { read: () => THEME, subscribe: () => () => {} },
    renderSlot: () => null,
    onChange: () => {
      if (!runner) return
      for (const page of runner.pages()) {
        if (page.problem && record.refusedTrees.at(-1)?.problem !== page.problem) record.refusedTrees.push({ page: page.pageId, problem: page.problem })
      }
    },
  }
  runner = new host.PluginHost(detail, grants, deps, storage.values())

  try {
    await play(runner, points, pkg.id, events, options.commands ?? [], seconds, problems, headless, () => worker, options.keys ?? [])
  } catch (error) {
    problems.push(error instanceof Error ? error.message : String(error))
  }
  // Read everything before stopping: stop() takes it all back.
  const runtime = runner.runtime()
  const pages = [...runner.pages()].map((page) => ({
    id: page.pageId, title: page.title, ...(page.icon ? { icon: page.icon } : {}), ...(page.badge ? { badge: page.badge } : {}), tree: page.tree, problem: page.problem,
  }))
  const snapshot = points.snapshot()
  const stored = storage.values()
  runner.stop()
  await fixture.stop()

  const reason = tidy(runtime.reason)
  if (runtime.state === 'crashed') problems.push(`Crashed: ${reason}`)
  else if (!['running', 'active'].includes(runtime.state)) problems.push(`Did not start: the state is "${runtime.state}"${reason ? ` (${reason})` : ''}.`)
  if (runtime.faults > 0) problems.push(`${runtime.faults} fault${runtime.faults === 1 ? '' : 's'}: see the log.`)
  for (const refused of record.refusedTrees) problems.push(`Refused tree on page "${refused.page}": ${refused.problem}`)
  const { drawProblems, ...extended } = headless.record
  for (const problem of drawProblems) problems.push(`Refused draw ${problem}`)
  return {
    ok: problems.length === 0,
    problems,
    notes,
    state: runtime.state,
    reason,
    faults: runtime.faults,
    grants,
    pages,
    commands: snapshot.commands.map((command) => ({ id: command.id.slice(plugin.pluginOwner(pkg.id).length + 1), title: command.title, group: command.group, ...(command.shortcut ? { shortcut: command.shortcut } : {}) })),
    status: snapshot.status.map((status) => ({ id: status.id, text: status.text, ...(status.tone ? { tone: status.tone } : {}) })),
    themes: snapshot.themes.map((theme) => ({ name: theme.name, scheme: theme.scheme, tokens: Object.keys(theme.tokens).length })),
    styles: snapshot.styles.length,
    storage: stored,
    ...record,
    ...extended,
    logs: runtime.logs.map((entry) => ({ level: entry.level, text: tidy(entry.text) })),
  }
}
