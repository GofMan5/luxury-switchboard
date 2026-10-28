import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ExtensionPoints } from '../../../shared/contracts/extensions'
import { PAYLOAD_BYTES } from '../domain/manifest'
import type { PluginPermission } from '../domain/permissions'
import type { PluginDetail } from '../domain/plugin'
import { RUNTIME_LIMITS } from '../domain/protocol'
import { DATA_LIMITS } from '../domain/specs'
import { HOST_LIMITS, PluginHost } from './plugin-host'
import type { ConfirmRequest } from './ports'
import { FakeControl, FakeWorkers, MemoryPluginsPort, detail, hostExtras, type FakeWorker } from './test-fakes'

const ALL_BASE: PluginPermission[] = ['ui:theme', 'ui:css', 'ui:pages', 'ui:status', 'ui:commands', 'ui:notify', 'control:read', 'control:write', 'storage']

const ALL: PluginPermission[] = [...ALL_BASE, 'ui:clipboard']

function setup(
  grants: readonly PluginPermission[] = ALL,
  plugin: PluginDetail = detail({ permissions: [...grants], granted: [...grants] }),
  initial: Record<string, string> | null = null,
) {
  const workers = new FakeWorkers()
  const control = new FakeControl()
  const storage = new MemoryPluginsPort()
  if (initial) storage.values.set(plugin.plugin.id, { ...initial })
  const extensions = new ExtensionPoints()
  const notify = vi.fn()
  const navigate = vi.fn()
  const onChange = vi.fn()
  const confirm = vi.fn(async (_request: ConfirmRequest, _signal: AbortSignal) => true)
  const clipboard = vi.fn(async (_text: string) => undefined)
  const extras = hostExtras()
  const host = new PluginHost(plugin, grants, {
    ...extras, workers, control, storage, extensions, notify, navigate, onChange, confirm, clipboard,
    isRoute: (route) => route === 'overview' || route === 'plugin/acme.tools/board',
    app: { version: '1.0.0', capabilities: ['plugins.manage'] },
  }, initial)
  return { host, workers, control, storage, extensions, notify, navigate, onChange, confirm, clipboard, ...extras }
}

async function running(grants?: readonly PluginPermission[], plugin?: PluginDetail, initial: Record<string, string> | null = null) {
  const env = setup(grants, plugin, initial)
  env.host.start()
  const worker = env.workers.last
  worker.send({ type: 'ready' })
  return { ...env, worker }
}

describe('PluginHost', () => {
  beforeEach(() => { vi.useFakeTimers() })
  afterEach(() => { vi.useRealTimers() })

  it('boots the worker with its identity and the effective grants, then activates it', () => {
    const { host, workers, onChange } = setup(['ui:pages'])
    host.start()
    expect(host.runtime().state).toBe('starting')
    expect(workers.last.context).toEqual({
      plugin: { id: 'acme.tools', name: 'Acme tools', version: '1.0.0', granted: ['ui:pages'] },
      app: { version: '1.0.0', capabilities: ['plugins.manage'] },
      settings: {},
      secrets: [],
    })
    workers.last.send({ type: 'ready' })
    expect(host.runtime().state).toBe('running')
    expect(workers.last.posted).toContainEqual({ type: 'activate' })
    expect(onChange).toHaveBeenCalled()
  })

  it('applies a theme and stylesheet only when granted, without starting a worker for a script-less plugin', () => {
    const theme = { name: 'Dusk', scheme: 'dark' as const, tokens: { '--accent': '#8899ff' } }
    const plugin = detail({ permissions: ['ui:theme', 'ui:css'] }, { script: '', css: 'body{}', theme })
    const granted = setup(['ui:theme', 'ui:css'], plugin)
    granted.host.start()
    expect(granted.workers.created).toHaveLength(0)
    expect(granted.host.runtime().state).toBe('active')
    expect(granted.extensions.snapshot().themes).toEqual([{ id: 'plugin:acme.tools', name: 'Dusk', scheme: 'dark', tokens: theme.tokens }])
    expect(granted.extensions.snapshot().styles).toEqual([{ owner: 'plugin:acme.tools', css: 'body{}' }])

    const withheld = setup(['ui:theme'], plugin)
    withheld.host.start()
    expect(withheld.extensions.snapshot().styles).toEqual([])
    expect(withheld.extensions.snapshot().themes).toHaveLength(1)
  })

  it('registers pages, renders checked trees and keeps the last good tree on a bad render', async () => {
    const { host, worker, extensions } = await running()
    expect((await worker.call('ui.registerPage', { id: 'board', title: 'Board', icon: 'gauge' }))).toMatchObject({ ok: true, value: { route: 'plugin/acme.tools/board' } })
    expect(extensions.snapshot().pages).toEqual([{ id: 'acme.tools/board', title: 'Board', icon: 'gauge' }])

    expect(await worker.call('ui.render', { pageId: 'board', tree: { type: 'text', text: 'Hello' } })).toMatchObject({ ok: true })
    const bad = await worker.call('ui.render', { pageId: 'board', tree: { type: 'script', src: 'x' } })
    expect(bad).toMatchObject({ ok: false, error: { code: 'invalid_tree' } })
    const [page] = [...host.pages()]
    expect(page.tree).toMatchObject({ type: 'text', text: 'Hello' })
    expect(page.problem).toMatch(/is not a node type/u)

    expect(await worker.call('ui.render', { pageId: 'other', tree: { type: 'divider' } })).toMatchObject({ ok: false, error: { code: 'not_found' } })
  })

  it('caps pages, commands and status readouts', async () => {
    const { worker } = await running()
    for (let index = 0; index < RUNTIME_LIMITS.pages; index += 1) expect(await worker.call('ui.registerPage', { id: `p${index}`, title: 'P' })).toMatchObject({ ok: true })
    expect(await worker.call('ui.registerPage', { id: 'one-more', title: 'P' })).toMatchObject({ ok: false, error: { code: 'limit' } })
    expect(await worker.call('ui.registerPage', { id: 'p0', title: 'Renamed' })).toMatchObject({ ok: true })

    for (let index = 0; index < RUNTIME_LIMITS.status; index += 1) expect(await worker.call('ui.setStatus', { id: `s${index}`, status: { text: 'x' } })).toMatchObject({ ok: true })
    expect(await worker.call('ui.setStatus', { id: 'extra', status: { text: 'x' } })).toMatchObject({ ok: false, error: { code: 'limit' } })
    expect(await worker.call('ui.setStatus', { id: 's0', status: null })).toMatchObject({ ok: true })
    expect(await worker.call('ui.setStatus', { id: 'extra', status: { text: 'x' } })).toMatchObject({ ok: true })
  })

  it('labels commands and status readouts with the plugin name and runs commands in the worker', async () => {
    const { worker, extensions } = await running()
    expect(await worker.call('commands.register', { id: 'go', title: 'Go north', group: 'Relay' })).toMatchObject({ ok: true, value: { id: 'plugin:acme.tools:go' } })
    const [command] = extensions.snapshot().commands
    expect(command).toMatchObject({ id: 'plugin:acme.tools:go', group: 'Acme tools · Relay', keywords: ['plugin', 'Acme tools'] })
    await command.run()
    expect(worker.posted).toContainEqual({ type: 'command', id: 'go' })

    await worker.call('ui.setStatus', { id: 'rpm', status: { text: '12 rpm', tooltip: 'Requests per minute' } })
    expect(extensions.snapshot().status).toEqual([{ id: 'plugin:acme.tools:rpm', text: '12 rpm', tooltip: 'Requests per minute\n— Acme tools' }])
  })

  it('refuses every call the plugin was not granted', async () => {
    const { worker, host } = await running(['ui:pages'])
    for (const [op, args] of [
      ['ui.setStatus', { id: 'a', status: { text: 'x' } }], ['commands.register', { id: 'a', title: 'A' }], ['notify', { title: 'Hi' }],
      ['storage.get', { key: 'a' }], ['control.call', { method: 'providers.list' }], ['events.subscribe', { topic: 'activity.changed' }],
    ] as const) {
      expect(await worker.call(op, args), op).toMatchObject({ ok: false, error: { code: 'permission_denied' } })
    }
    expect(host.runtime().logs.some((entry) => entry.level === 'warn' && entry.text.includes('control:read'))).toBe(true)
  })

  it('gates control calls by tier and never reaches the plugin store', async () => {
    const { worker, control } = await running(['control:read'])
    expect(await worker.call('control.call', { method: 'activity.summary', payload: { a: 1 } })).toMatchObject({ ok: true, value: { method: 'activity.summary' } })
    expect(control.call).toHaveBeenCalledWith('activity.summary', { a: 1 }, expect.any(AbortSignal))
    expect(await worker.call('control.call', { method: 'relay.stop' })).toMatchObject({ ok: false, error: { code: 'permission_denied' } })
    expect(await worker.call('control.call', { method: 'plugins.update', payload: { id: 'acme.tools', granted: ['control:sensitive'] } })).toMatchObject({ ok: false, error: { code: 'not_allowed' } })
    expect(control.call).toHaveBeenCalledTimes(1)
  })

  it('passes control-plane errors through with their code', async () => {
    const { worker, control } = await running(['control:write'])
    control.call.mockRejectedValueOnce(Object.assign(new Error('No such provider'), { code: 'provider_not_found' }))
    expect(await worker.call('control.call', { method: 'providers.activate', payload: { id: 'nope' } })).toEqual({ type: 'result', id: 1, ok: false, error: { code: 'provider_not_found', message: 'No such provider' } })
  })

  it('limits control calls in flight and aborts them when stopped', async () => {
    const { worker, control, host } = await running(['control:read'])
    const signals: AbortSignal[] = []
    control.call.mockImplementation((_method, _payload, signal) => {
      if (signal) signals.push(signal)
      return new Promise(() => undefined)
    })
    for (let index = 0; index < RUNTIME_LIMITS.inflight; index += 1) worker.send({ type: 'call', id: 1000 + index, op: 'control.call', args: { method: 'relay.status' } })
    await Promise.resolve()
    expect(await worker.call('control.call', { method: 'relay.status' })).toMatchObject({ ok: false, error: { code: 'busy' } })
    host.stop()
    expect(signals).toHaveLength(RUNTIME_LIMITS.inflight)
    expect(signals.every((signal) => signal.aborted)).toBe(true)
  })

  it('forwards topics it may follow and unsubscribes when stopped', async () => {
    const { worker, control, host } = await running(['control:read'])
    expect(await worker.call('events.subscribe', { topic: 'activity.changed' })).toMatchObject({ ok: true })
    expect(await worker.call('events.subscribe', { topic: 'plugins.changed' })).toMatchObject({ ok: false, error: { code: 'not_allowed' } })
    control.listeners.get('activity.changed')?.({ active: 2 })
    expect(worker.posted).toContainEqual({ type: 'topic', topic: 'activity.changed', payload: { active: 2 } })
    host.stop()
    expect(control.unsubscribed).toEqual(['activity.changed'])
  })

  it('keeps storage per plugin and checks keys and value sizes', async () => {
    const { worker, storage } = await running(['storage'])
    expect(await worker.call('storage.set', { key: 'period', value: '24h' })).toMatchObject({ ok: true })
    expect(await worker.call('storage.get', { key: 'period' })).toMatchObject({ ok: true, value: '24h' })
    expect(await worker.call('storage.all')).toMatchObject({ ok: true, value: { period: '24h' } })
    expect(storage.values.get('acme.tools')).toEqual({ period: '24h' })
    expect(await worker.call('storage.set', { key: 'n', value: 5 })).toMatchObject({ ok: false, error: { code: 'invalid_argument' } })
    expect(await worker.call('storage.set', { key: 'big', value: 'x'.repeat(8193) })).toMatchObject({ ok: false, error: { code: 'too_large' } })
    expect(await worker.call('storage.get', { key: '../other' })).toMatchObject({ ok: false, error: { code: 'invalid_argument' } })
    expect(await worker.call('storage.remove', { key: 'period' })).toMatchObject({ ok: true })
    expect(await worker.call('storage.get', { key: 'period' })).toMatchObject({ ok: true, value: null })
  })

  it('signs notifications and rate-limits them per minute', async () => {
    const { worker, notify } = await running(['ui:notify'])
    for (let index = 0; index < RUNTIME_LIMITS.notifyPerMinute; index += 1) expect(await worker.call('notify', { title: 'Hi', body: 'There', tone: 'success' })).toMatchObject({ ok: true })
    expect(notify).toHaveBeenLastCalledWith({ kind: 'plugin', severity: 'success', title: 'Hi', body: 'There\n— Acme tools' })
    expect(await worker.call('notify', { title: 'Again' })).toMatchObject({ ok: false, error: { code: 'rate_limited' } })
    // Move the clock only: running the timers would also run the liveness
    // check, which this worker never answers.
    vi.setSystemTime(Date.now() + 60_001)
    expect(await worker.call('notify', { title: 'Later' })).toMatchObject({ ok: true })
  })

  it('hides upstream URLs and auth header names from plugins without control:sensitive', async () => {
    const provider = { id: 'p1', name: 'Main', baseUrl: 'https://upstream.example.test/v1', authHeader: 'X-Api-Key', authMode: 'header', modelsPath: '/models', chatPath: '/chat', enabled: true }
    const listed = { activeId: 'p1', providers: [provider] }
    const readOnly = await running(['control:read'])
    readOnly.control.call.mockResolvedValueOnce(listed)
    expect(await readOnly.worker.call('control.call', { method: 'providers.list' })).toMatchObject({ ok: true, value: { activeId: 'p1', providers: [{ id: 'p1', name: 'Main', authMode: 'header', enabled: true }] } })
    expect(JSON.stringify(readOnly.worker.posted)).not.toMatch(/upstream\.example|X-Api-Key|baseUrl|authHeader|modelsPath|chatPath/u)
    await readOnly.worker.call('events.subscribe', { topic: 'providers.changed' })
    readOnly.control.listeners.get('providers.changed')?.({ activeId: 'p1', provider })
    expect(readOnly.worker.posted.at(-1)).toEqual({ type: 'topic', topic: 'providers.changed', payload: { activeId: 'p1', provider: { id: 'p1', name: 'Main', authMode: 'header', enabled: true } } })

    const sensitive = await running(['control:read', 'control:sensitive'])
    sensitive.control.call.mockResolvedValueOnce(listed)
    expect(await sensitive.worker.call('control.call', { method: 'providers.list' })).toMatchObject({ ok: true, value: listed })
    await sensitive.worker.call('events.subscribe', { topic: 'providers.changed' })
    sensitive.control.listeners.get('providers.changed')?.({ activeId: 'p1', provider })
    expect(sensitive.worker.posted.at(-1)).toEqual({ type: 'topic', topic: 'providers.changed', payload: { activeId: 'p1', provider } })
  })

  it('maps a sidecar command_too_large to too_large with the payload budget', async () => {
    const { worker, control } = await running(['control:read'])
    control.call.mockRejectedValueOnce(Object.assign(new Error('Command payload exceeds 258048 bytes'), { code: 'command_too_large' }))
    expect(await worker.call('control.call', { method: 'activity.summary', payload: {} })).toMatchObject({
      ok: false, error: { code: 'too_large', message: `The call is too large for one Switchboard command (at most ${Math.floor(PAYLOAD_BYTES / 1024)} KiB of payload, including what the host adds). Send less at once.` },
    })
  })

  it('does not count a declined question as the operator acting', async () => {
    const { worker, host, confirm } = await running(['ui:pages', 'ui:notify'])
    await worker.call('ui.registerPage', { id: 'board', title: 'Board' })
    host.dispatch('board', { kind: 'action', action: 'delete', form: {} })
    vi.setSystemTime(Date.now() + RUNTIME_LIMITS.gestureMs - 1)
    confirm.mockResolvedValueOnce(false)
    expect(await worker.call('ui.confirm', { title: 'Delete?' })).toMatchObject({ ok: true, value: false })
    vi.setSystemTime(Date.now() + 2)
    expect(await worker.call('ui.confirm', { title: 'Really?' })).toMatchObject({ ok: false, error: { code: 'needs_gesture' } })
  })

  it('navigates only to routes the shell has', async () => {
    const { worker, navigate } = await running([])
    expect(await worker.call('navigate', { route: '#overview' })).toMatchObject({ ok: true })
    expect(navigate).toHaveBeenCalledWith('overview')
    expect(await worker.call('navigate', { route: '/overview' })).toMatchObject({ ok: true })
    expect(await worker.call('navigate', { route: '#/overview' })).toMatchObject({ ok: true })
    expect(navigate.mock.calls).toEqual([['overview'], ['overview'], ['overview']])
    expect(await worker.call('navigate', { route: 'https://example.test' })).toMatchObject({ ok: false, error: { code: 'not_found' } })
  })

  it('refuses oversized arguments without stopping the plugin', async () => {
    const { worker, host } = await running()
    const result = await worker.call('ui.registerPage', { id: 'a', title: 'A', padding: 'x'.repeat(HOST_LIMITS.argsBytes) })
    expect(result).toMatchObject({ ok: false, error: { code: 'too_large' } })
    expect(host.runtime().state).toBe('running')
  })

  it('stops a plugin that breaks the protocol or floods the window', async () => {
    const broken = await running()
    broken.worker.send({ type: 'eval', code: '1' })
    expect(broken.host.runtime()).toMatchObject({ state: 'crashed', reason: 'Stopped for sending a message outside the plugin protocol.' })
    expect(broken.worker.terminated).toBe(true)

    const flood = await running()
    for (let index = 0; index < HOST_LIMITS.messages + 1; index += 1) flood.worker.send({ type: 'pong', seq: index })
    expect(flood.host.runtime().state).toBe('crashed')
    expect(flood.host.runtime().reason).toMatch(/flooding/u)
  })

  it('stops a script that does not start in time or stops answering pings', () => {
    const slow = setup()
    slow.host.start()
    vi.advanceTimersByTime(HOST_LIMITS.startMs)
    expect(slow.host.runtime()).toMatchObject({ state: 'crashed', reason: expect.stringContaining('did not finish starting') })

    const stuck = setup()
    stuck.host.start()
    stuck.workers.last.send({ type: 'ready' })
    vi.advanceTimersByTime(HOST_LIMITS.unresponsiveMs + HOST_LIMITS.pingMs)
    expect(stuck.host.runtime()).toMatchObject({ state: 'crashed', reason: expect.stringContaining('stopped responding') })
  })

  it('stays alive while the worker answers pings', () => {
    const { host, workers } = setup()
    host.start()
    const worker = workers.last
    worker.send({ type: 'ready' })
    for (let tick = 0; tick < 10; tick += 1) {
      vi.advanceTimersByTime(HOST_LIMITS.pingMs)
      const ping = worker.posted.findLast((message) => message.type === 'ping')
      if (ping?.type === 'ping') worker.send({ type: 'pong', seq: ping.seq })
    }
    expect(host.runtime().state).toBe('running')
  })

  it('counts faults it survives and crashes on a start failure or worker error', async () => {
    const env = await running()
    env.worker.send({ type: 'fault', message: 'handler threw' })
    expect(env.host.runtime()).toMatchObject({ state: 'running', faults: 1 })

    const failing = setup()
    failing.host.start()
    failing.workers.last.send({ type: 'fail', message: 'ReferenceError: x is not defined' })
    expect(failing.host.runtime().reason).toBe('The script failed while starting: ReferenceError: x is not defined')

    const erroring = await running()
    erroring.worker.fail('Uncaught SyntaxError')
    expect(erroring.host.runtime()).toMatchObject({ state: 'crashed', reason: 'Uncaught SyntaxError' })
  })

  it('takes back what the script added on a crash but keeps the theme, and everything on stop', async () => {
    const theme = { name: 'Dusk', scheme: 'dark' as const, tokens: { '--accent': '#8899ff' } }
    const grants: PluginPermission[] = ['ui:theme', 'ui:pages', 'ui:commands', 'ui:status']
    const env = await running(grants, detail({ permissions: grants, granted: grants }, { theme }))
    await env.worker.call('ui.registerPage', { id: 'board', title: 'Board' })
    await env.worker.call('commands.register', { id: 'go', title: 'Go' })
    await env.worker.call('ui.setStatus', { id: 'rpm', status: { text: '1' } })
    env.worker.send({ type: 'nonsense' })
    const after = env.extensions.snapshot()
    expect([after.pages, after.commands, after.status]).toEqual([[], [], []])
    expect(after.themes).toHaveLength(1)
    expect([...env.host.pages()]).toEqual([])

    env.host.stop()
    expect(env.extensions.snapshot().themes).toEqual([])
    expect(env.host.runtime().state).toBe('stopped')
  })

  it('ignores messages from a worker it already replaced', async () => {
    const { worker, host } = await running()
    host.stop()
    worker.send({ type: 'eval' })
    expect(host.runtime().state).toBe('stopped')
  })

  it('badges registered pages, keeps the badge across a re-registration and clears it', async () => {
    const { worker, extensions } = await running(['ui:pages'])
    expect(await worker.call('ui.setBadge', { pageId: 'board', badge: { text: '3' } })).toMatchObject({ ok: false, error: { code: 'not_found' } })
    await worker.call('ui.registerPage', { id: 'board', title: 'Board' })
    expect(await worker.call('ui.setBadge', { pageId: 'board', badge: { text: '3', tone: 'warning' } })).toMatchObject({ ok: true })
    expect(extensions.snapshot().pages).toEqual([{ id: 'acme.tools/board', title: 'Board', badge: { text: '3', tone: 'warning' } }])
    await worker.call('ui.registerPage', { id: 'board', title: 'Board 2' })
    expect(extensions.snapshot().pages[0]).toMatchObject({ title: 'Board 2', badge: { text: '3' } })
    expect(await worker.call('ui.setBadge', { pageId: 'board', badge: { text: '123456789' } })).toMatchObject({ ok: false, error: { code: 'invalid_argument' } })
    expect(await worker.call('ui.setBadge', { pageId: 'board', badge: null })).toMatchObject({ ok: true })
    expect(extensions.snapshot().pages).toEqual([{ id: 'acme.tools/board', title: 'Board 2' }])
  })

  it('asks the operator only right after they acted, one question at a time', async () => {
    const { worker, host, confirm } = await running(['ui:pages', 'ui:notify'])
    expect(await worker.call('ui.confirm', { title: 'Delete?' })).toMatchObject({ ok: false, error: { code: 'needs_gesture' } })
    await worker.call('ui.registerPage', { id: 'board', title: 'Board' })
    host.dispatch('board', { kind: 'action', action: 'delete', form: {} })
    let answer: (yes: boolean) => void = () => undefined
    confirm.mockImplementationOnce(() => new Promise<boolean>((resolve) => { answer = resolve }))
    worker.send({ type: 'call', id: 500, op: 'ui.confirm', args: { title: 'Delete?', tone: 'danger', confirmText: 'Delete' } })
    await Promise.resolve()
    expect(confirm).toHaveBeenCalledWith({ pluginName: 'Acme tools', title: 'Delete?', body: '', confirmText: 'Delete', tone: 'danger' }, expect.any(AbortSignal))
    expect(await worker.call('ui.confirm', { title: 'Again?' })).toMatchObject({ ok: false, error: { code: 'busy' } })
    // The operator takes most of the gesture window to answer; the answer
    // itself counts as acting, so a follow-up is still allowed.
    vi.setSystemTime(Date.now() + RUNTIME_LIMITS.gestureMs - 1)
    answer(true)
    for (let turn = 0; turn < 10; turn += 1) await Promise.resolve()
    expect(worker.posted).toContainEqual({ type: 'result', id: 500, ok: true, value: true })
    vi.setSystemTime(Date.now() + RUNTIME_LIMITS.gestureMs - 1)
    expect(await worker.call('ui.confirm', { title: 'Sure?' })).toMatchObject({ ok: true, value: true })
    vi.setSystemTime(Date.now() + RUNTIME_LIMITS.gestureMs + 1)
    expect(await worker.call('ui.confirm', { title: 'Late?' })).toMatchObject({ ok: false, error: { code: 'needs_gesture' } })
  })

  it('aborts an open question when the plugin stops', async () => {
    const { worker, host, confirm, extensions } = await running(['ui:commands', 'ui:notify'])
    await worker.call('commands.register', { id: 'go', title: 'Go' })
    await extensions.snapshot().commands[0].run()
    let signal: AbortSignal | undefined
    confirm.mockImplementationOnce((_request, given) => { signal = given; return new Promise<boolean>(() => undefined) })
    worker.send({ type: 'call', id: 77, op: 'ui.confirm', args: { title: 'Go?' } })
    await Promise.resolve()
    host.stop()
    expect(signal?.aborted).toBe(true)
  })

  it('does not treat a page being shown or hidden as the operator acting', async () => {
    const { host, worker } = await running(['ui:pages', 'ui:clipboard'])
    await worker.call('ui.registerPage', { id: 'board', title: 'Board' })
    host.dispatch('board', { kind: 'show', form: {} })
    host.dispatch('board', { kind: 'hide', form: {} })
    expect(await worker.call('clipboard.writeText', { text: 'x' })).toMatchObject({ ok: false, error: { code: 'needs_gesture' } })
    host.dispatch('board', { kind: 'change', form: {} })
    expect(await worker.call('clipboard.writeText', { text: 'x' })).toMatchObject({ ok: true })
  })

  it('writes the clipboard only when granted and right after a command', async () => {
    const denied = await running(['ui:commands'])
    expect(await denied.worker.call('clipboard.writeText', { text: 'x' })).toMatchObject({ ok: false, error: { code: 'permission_denied' } })

    const { worker, extensions, clipboard } = await running(['ui:commands', 'ui:clipboard'])
    expect(await worker.call('clipboard.writeText', { text: 'x' })).toMatchObject({ ok: false, error: { code: 'needs_gesture' } })
    await worker.call('commands.register', { id: 'copy', title: 'Copy' })
    await extensions.snapshot().commands[0].run()
    expect(await worker.call('clipboard.writeText', { text: 'sk-fictional' })).toMatchObject({ ok: true })
    expect(clipboard).toHaveBeenCalledWith('sk-fictional')
    expect(await worker.call('clipboard.writeText', { text: 'x'.repeat(RUNTIME_LIMITS.clipboardText + 1) })).toMatchObject({ ok: false, error: { code: 'too_large' } })
    clipboard.mockRejectedValueOnce(new Error('Document is not focused'))
    expect(await worker.call('clipboard.writeText', { text: 'y' })).toMatchObject({ ok: false, error: { code: 'failed', message: expect.stringContaining('not focused') } })
  })

  it('binds command shortcuts and refuses one another plugin holds', async () => {
    const { worker, extensions } = await running(['ui:commands'])
    extensions.contribute('plugin:other.tools', { commands: [{ id: 'plugin:other.tools:x', title: 'Other', group: 'Other tools', shortcut: 'Alt+K', bind: true, run: () => undefined }] })
    expect(await worker.call('commands.register', { id: 'go', title: 'Go', shortcut: 'alt+k' })).toMatchObject({ ok: false, error: { code: 'conflict', message: expect.stringContaining('Other') } })
    expect(await worker.call('commands.register', { id: 'go', title: 'Go', shortcut: 'K' })).toMatchObject({ ok: false, error: { code: 'invalid_argument' } })
    expect(await worker.call('commands.register', { id: 'go', title: 'Go', shortcut: 'ctrl+shift+g' })).toMatchObject({ ok: true })
    expect(extensions.snapshot().commands.find((command) => command.id === 'plugin:acme.tools:go')).toMatchObject({ shortcut: 'Ctrl+Shift+G', bind: true })
    // Registering the same command again keeps its own shortcut.
    expect(await worker.call('commands.register', { id: 'go', title: 'Go again', shortcut: 'Ctrl+Shift+G' })).toMatchObject({ ok: true })
  })

  it('turns a failing command into one toast for the operator who ran it', async () => {
    const { worker, extensions, notify, host } = await running(['ui:commands'])
    await worker.call('commands.register', { id: 'sync', title: 'Sync now' })
    worker.send({ type: 'fault', message: 'boom', command: 'sync' })
    expect(notify).not.toHaveBeenCalled()
    await extensions.snapshot().commands[0].run()
    worker.send({ type: 'fault', message: 'TypeError: boom', command: 'sync' })
    worker.send({ type: 'fault', message: 'again', command: 'sync' })
    expect(notify).toHaveBeenCalledTimes(1)
    expect(notify).toHaveBeenCalledWith({ kind: 'plugin', severity: 'danger', title: 'Sync now failed', body: 'TypeError: boom\n— Acme tools' })
    expect(host.runtime().faults).toBe(3)
  })

  it('writes storage batches atomically, in order, within a budget, and keeps settings out of reach', async () => {
    const { worker, storage } = await running(['storage'], undefined, { 'setting:limit': '5', kept: '1' })
    expect(await worker.call('storage.all')).toMatchObject({ ok: true, value: { kept: '1' } })
    expect(await worker.call('storage.get', { key: 'setting:limit' })).toMatchObject({ ok: false, error: { code: 'invalid_argument' } })
    expect(await worker.call('storage.setMany', { values: { a: '1', b: '2', kept: null } })).toMatchObject({ ok: true })
    expect(storage.values.get('acme.tools')).toEqual({ 'setting:limit': '5', a: '1', b: '2' })
    expect(await worker.call('storage.setMany', { values: { a: '9', 'setting:limit': '1' } })).toMatchObject({ ok: false, error: { code: 'invalid_argument' } })
    expect(storage.values.get('acme.tools')).toMatchObject({ a: '1' })
    expect(await worker.call('storage.setMany', { values: {} })).toMatchObject({ ok: false, error: { code: 'invalid_argument' } })

    expect(await worker.call('storage.clear')).toMatchObject({ ok: true })
    expect(storage.values.get('acme.tools')).toEqual({ 'setting:limit': '5' })

    // Writes issued together land in the order they were made.
    for (let index = 0; index < 5; index += 1) worker.send({ type: 'call', id: 900 + index, op: 'storage.set', args: { key: 'n', value: String(index) } })
    await vi.waitFor(() => expect(worker.posted.filter((message) => message.type === 'result' && message.id >= 900)).toHaveLength(5))
    expect(storage.values.get('acme.tools')?.n).toBe('4')
  })

  it('rate-limits storage writes per minute', async () => {
    const { worker } = await running(['storage'])
    for (let index = 0; index < RUNTIME_LIMITS.storageWritesPerMinute; index += 1) worker.send({ type: 'call', id: 2000 + index, op: 'storage.set', args: { key: 'k', value: String(index) } })
    await vi.waitFor(() => expect(worker.posted.filter((message) => message.type === 'result' && message.id >= 2000)).toHaveLength(RUNTIME_LIMITS.storageWritesPerMinute))
    expect(worker.posted.every((message) => message.type !== 'result' || message.ok)).toBe(true)
    expect(await worker.call('storage.set', { key: 'k', value: 'late' })).toMatchObject({ ok: false, error: { code: 'rate_limited' } })
    vi.setSystemTime(Date.now() + 60_001)
    expect(await worker.call('storage.set', { key: 'k', value: 'later' })).toMatchObject({ ok: true })
  })

  it('does not spend the write budget on writes that failed', async () => {
    const { worker, storage } = await running(['storage'])
    for (let index = 0; index < RUNTIME_LIMITS.storageWritesPerMinute; index += 1) {
      storage.failNext = new Error('Storage is unavailable')
      expect(await worker.call('storage.set', { key: 'k', value: String(index) })).toMatchObject({ ok: false })
    }
    expect(await worker.call('storage.set', { key: 'k', value: 'landed' })).toMatchObject({ ok: true })
  })

  it('boots with the operator settings and tells the script when they change', async () => {
    const settings = [
      { key: 'limit', label: 'Limit', description: '', type: 'number' as const, default: 10, placeholder: '' },
      { key: 'compact', label: 'Compact', description: '', type: 'toggle' as const, default: false },
    ]
    const plugin = detail({ permissions: [], granted: [] }, { settings })
    const { host, workers } = setup([], plugin, { 'setting:limit': '25', 'setting:compact': '"nonsense"' })
    host.start()
    expect(workers.last.context.settings).toEqual({ limit: 25, compact: false })
    workers.last.send({ type: 'ready' })
    host.storageChanged({ 'setting:limit': '25', 'setting:compact': 'true' })
    expect(workers.last.posted).toContainEqual({ type: 'settings', values: { limit: 25, compact: true }, secrets: [] })
    const before = workers.last.posted.length
    host.storageChanged({ 'setting:limit': '25', 'setting:compact': 'true', unrelated: 'x' })
    host.secretsChanged([])
    expect(workers.last.posted).toHaveLength(before)
    host.secretsChanged(['token'])
    expect(workers.last.posted.at(-1)).toEqual({ type: 'settings', values: { limit: 25, compact: true }, secrets: ['token'] })
  })

  it('delivers page events only for registered pages once running', async () => {
    const { worker, host } = await running()
    host.dispatch('board', { kind: 'show', form: {} })
    expect(worker.posted.filter((message) => message.type === 'ui')).toHaveLength(0)
    await worker.call('ui.registerPage', { id: 'board', title: 'Board' })
    host.dispatch('board', { kind: 'action', action: 'refresh', form: { q: 'x' } })
    expect(worker.posted).toContainEqual({ type: 'ui', pageId: 'board', event: { kind: 'action', action: 'refresh', form: { q: 'x' } } })
  })
})

const DATA: PluginPermission[] = ['ui:pages', 'ui:slots', 'ui:layout', 'ui:dialogs', 'ui:sound', 'files', 'bus', 'net:fetch', 'storage', 'relay:rules', 'relay:content', 'control:read']

/** A second plugin in the same window: same contributions and bus. */
function neighbour(env: ReturnType<typeof setup>, grants: readonly PluginPermission[] = DATA) {
  const workers = new FakeWorkers()
  const plugin = detail({ id: 'other.tools', name: 'Other tools', permissions: [...grants], granted: [...grants] })
  const host = new PluginHost(plugin, grants, {
    ...hostExtras(env.bus), workers, control: new FakeControl(), storage: new MemoryPluginsPort(), extensions: env.extensions,
    notify: vi.fn(), navigate: vi.fn(), onChange: vi.fn(), confirm: vi.fn(async () => true), clipboard: vi.fn(async () => undefined),
    isRoute: () => true, app: { version: '1.0.0', capabilities: ['plugins.manage'] },
  }, null)
  host.start()
  workers.last.send({ type: 'ready' })
  return { host, worker: workers.last }
}

/** The operator acts on the plugin's page, opening the gesture window. */
async function act(host: PluginHost, worker: FakeWorker) {
  await worker.call('ui.registerPage', { id: 'board', title: 'Board' })
  host.dispatch('board', { kind: 'action', action: 'go', form: {} })
}

async function settle() { for (let turn = 0; turn < 10; turn += 1) await Promise.resolve() }

const URL_X = { url: 'https://api.example.com/x' }

describe('PluginHost data and surface APIs', () => {
  beforeEach(() => { vi.useFakeTimers() })
  afterEach(() => { vi.useRealTimers() })

  it('contributes slot surfaces and actions and removes either by id', async () => {
    const { host, worker, extensions, renderSlot } = await running(DATA)
    expect(await worker.call('ui.registerWidget', { id: 'load', title: 'Load', wide: true })).toMatchObject({ ok: true, value: { surface: 'widget:load' } })
    expect(await worker.call('ui.registerAction', { id: 'retry', title: 'Retry', scope: 'activity' })).toMatchObject({ ok: true })
    const [slot] = extensions.snapshot().slots
    expect(slot).toMatchObject({ owner: 'Acme tools', scope: 'overview', title: 'Load', wide: true })
    slot?.render({ scope: 'overview' })
    expect(renderSlot).toHaveBeenCalledWith('acme.tools', 'widget:load', { scope: 'overview' })
    expect(host.surfaces()).toEqual([{ key: 'acme.tools/widget:load', pluginId: 'acme.tools', surface: 'widget:load', tree: null, problem: '' }])
    expect(extensions.snapshot().actions).toMatchObject([{ owner: 'Acme tools', scope: 'activity', title: 'Retry' }])

    await worker.call('ui.unregister', { id: 'widget:load' })
    await worker.call('ui.unregister', { id: 'retry' })
    expect(extensions.snapshot()).toMatchObject({ slots: [], actions: [] })
    expect(host.surfaces()).toEqual([])

    const { worker: bare } = await running(['ui:pages'])
    expect(await bare.call('ui.registerWidget', { id: 'load', title: 'Load' })).toMatchObject({ ok: false, error: { code: 'permission_denied' } })
  })

  it('lets one plugin own the layout at a time', async () => {
    const env = await running(DATA)
    const other = neighbour(env)
    expect(await env.worker.call('ui.setLayout', { hidden: ['keys'] })).toMatchObject({ ok: true })
    expect(env.extensions.snapshot().layout).toMatchObject({ owner: 'Acme tools', hidden: ['keys'] })
    expect(await other.worker.call('ui.setLayout', { hidden: ['activity'] })).toMatchObject({ ok: false, error: { code: 'conflict' } })
    env.extensions.snapshot().layout?.reset()
    expect(env.extensions.snapshot().layout).toBeUndefined()
    expect(await other.worker.call('ui.setLayout', { hidden: ['activity'] })).toMatchObject({ ok: true })
    expect(env.extensions.snapshot().layout).toMatchObject({ owner: 'Other tools' })
  })

  it('opens one dialog after a gesture, keeps the last good tree and returns the answer', async () => {
    const { host, worker } = await running(DATA)
    const spec = { title: 'Rename', tree: { type: 'text', text: 'Name?' }, actions: [{ id: 'ok', label: 'OK', tone: 'primary' }] }
    expect(await worker.call('ui.dialog', spec)).toMatchObject({ ok: false, error: { code: 'needs_gesture' } })
    await act(host, worker)
    expect(await worker.call('ui.dialog', spec)).toMatchObject({ ok: true, value: { id: 'd1' } })
    expect(host.dialog()).toMatchObject({ id: 'd1', title: 'Rename', pluginName: 'Acme tools', tree: { type: 'text', text: 'Name?' } })
    expect(await worker.call('ui.dialog', spec)).toMatchObject({ ok: false, error: { code: 'busy' } })

    expect(await worker.call('ui.dialogUpdate', { id: 'd1', tree: { type: 'script' } })).toMatchObject({ ok: false, error: { code: 'invalid_tree' } })
    expect(host.dialog()).toMatchObject({ tree: { type: 'text', text: 'Name?' }, problem: expect.stringMatching(/node type/u) })

    host.answerDialog('d1', 'not-a-button', {})
    expect(host.dialog()).not.toBeNull()
    host.answerDialog('d1', 'ok', { name: 'A' })
    expect(host.dialog()).toBeNull()
    expect(worker.posted).toContainEqual({ type: 'dialog', id: 'd1', result: { action: 'ok', form: { name: 'A' } } })
    expect(await worker.call('ui.dialogUpdate', { id: 'd1', tree: { type: 'divider' } })).toMatchObject({ ok: false, error: { code: 'not_found' } })
  })

  it('limits requests in flight and per minute', async () => {
    const { worker, control } = await running(DATA)
    const pending: (() => void)[] = []
    const answer = { status: 200, headers: {}, body: '' }
    control.call.mockImplementation((method) => (method === 'plugins.fetch' && pending.length < DATA_LIMITS.fetchInflight
      ? new Promise((resolve) => { pending.push(() => resolve(answer)) })
      : Promise.resolve(answer)))
    for (let index = 0; index < DATA_LIMITS.fetchInflight; index += 1) worker.send({ type: 'call', id: 900 + index, op: 'net.fetch', args: URL_X })
    await settle()
    expect(await worker.call('net.fetch', URL_X)).toMatchObject({ ok: false, error: { code: 'rate_limited' } })
    expect(control.call).toHaveBeenCalledWith('plugins.fetch', expect.objectContaining({ id: 'acme.tools', url: URL_X.url, method: 'GET' }), expect.any(AbortSignal))
    for (const resolve of pending) resolve()
    await settle()
    // A call refused for being over the in-flight cap spends nothing.
    for (let index = DATA_LIMITS.fetchInflight; index < DATA_LIMITS.fetchPerMinute; index += 1) {
      expect(await worker.call('net.fetch', URL_X)).toMatchObject({ ok: true })
    }
    expect(await worker.call('net.fetch', URL_X)).toMatchObject({ ok: false, error: { code: 'rate_limited' } })
    vi.setSystemTime(Date.now() + 60_001)
    expect(await worker.call('net.fetch', URL_X)).toMatchObject({ ok: true })
    expect(await worker.call('net.fetch', { url: 'http://api.example.com/x' })).toMatchObject({ ok: false, error: { code: 'not_allowed' } })
  })

  it('unwraps collection answers and budgets collection writes', async () => {
    const { worker, control } = await running(DATA)
    const answers: Record<string, unknown> = {
      'plugins.collection.get': { doc: { a: 1 } }, 'plugins.collection.delete': { deleted: true }, 'plugins.collection.clear': { cleared: 3 },
      'plugins.collection.put': { ok: true }, 'plugins.collection.list': { items: [{ id: 'x', doc: 1 }] },
    }
    control.call.mockImplementation(async (method) => answers[method])
    expect(await worker.call('db.get', { collection: 'notes', docId: 'a' })).toMatchObject({ ok: true, value: { a: 1 } })
    expect(await worker.call('db.delete', { collection: 'notes', docId: 'a' })).toMatchObject({ ok: true, value: true })
    expect(await worker.call('db.clear', { collection: 'notes' })).toMatchObject({ ok: true, value: 3 })
    expect(await worker.call('db.list', { collection: 'notes' })).toMatchObject({ ok: true, value: { items: [{ id: 'x', doc: 1 }] } })
    expect(control.call).toHaveBeenCalledWith('plugins.collection.get', { id: 'acme.tools', collection: 'notes', docId: 'a' }, expect.any(AbortSignal))
    answers['plugins.collection.get'] = { doc: null }
    expect(await worker.call('db.get', { collection: 'notes', docId: 'b' })).toMatchObject({ ok: true, value: null })

    // delete and clear above were two writes.
    for (let index = 2; index < DATA_LIMITS.writesPerMinute; index += 1) await worker.call('db.put', { collection: 'notes', docId: `d${index}`, doc: { index } })
    expect(await worker.call('db.put', { collection: 'notes', docId: 'over', doc: 1 })).toMatchObject({ ok: false, error: { code: 'rate_limited' } })
  })

  it('reads a picked file only within the gesture window and its size cap', async () => {
    const { host, worker, files } = await running(DATA)
    files.next = { name: 'big.json', size: 2048, read: async () => '{}' }
    expect(await worker.call('files.open', {})).toMatchObject({ ok: false, error: { code: 'needs_gesture' } })
    await act(host, worker)
    expect(await worker.call('files.open', { maxBytes: 1024 })).toMatchObject({ ok: false, error: { code: 'too_large' } })
    files.next = { name: 'small.json', size: 2, read: async () => '{}' }
    expect(await worker.call('files.open', { accept: ['.json'] })).toMatchObject({ ok: true, value: { name: 'small.json', text: '{}', size: 2 } })
    expect(files.opened.at(-1)).toEqual({ accept: ['.json'], maxBytes: DATA_LIMITS.openBytes })
    expect(await worker.call('files.save', { name: 'out.json', text: '[]' })).toMatchObject({ ok: true, value: true })
    expect(files.saved).toEqual([{ name: 'out.json', text: '[]' }])
  })

  it('extends the gesture window only for a completed pick or save', async () => {
    const { host, worker, files } = await running(DATA)
    await act(host, worker)
    vi.setSystemTime(Date.now() + RUNTIME_LIMITS.gestureMs - 1)
    files.next = null
    expect(await worker.call('files.open', {})).toMatchObject({ ok: true, value: null })
    vi.setSystemTime(Date.now() + 2)
    expect(await worker.call('files.save', { name: 'a.json', text: '1' })).toMatchObject({ ok: false, error: { code: 'needs_gesture' } })

    await act(host, worker)
    vi.setSystemTime(Date.now() + RUNTIME_LIMITS.gestureMs - 1)
    files.saveAnswer = false
    expect(await worker.call('files.save', { name: 'a.json', text: '1' })).toMatchObject({ ok: true, value: false })
    vi.setSystemTime(Date.now() + 2)
    expect(await worker.call('files.save', { name: 'a.json', text: '1' })).toMatchObject({ ok: false, error: { code: 'needs_gesture' } })

    await act(host, worker)
    vi.setSystemTime(Date.now() + RUNTIME_LIMITS.gestureMs - 1)
    files.next = { name: 'n.json', size: 2, read: async () => '{}' }
    expect(await worker.call('files.open', {})).toMatchObject({ ok: true })
    vi.setSystemTime(Date.now() + 2)
    files.saveAnswer = true
    expect(await worker.call('files.save', { name: 'b.json', text: '2' })).toMatchObject({ ok: true, value: true })
  })

  it('carries bus messages between plugins but not back to the sender', async () => {
    const env = await running(DATA)
    const other = neighbour(env)
    await env.worker.call('bus.subscribe', { channel: 'news' })
    await other.worker.call('bus.subscribe', { channel: 'news' })
    expect(await other.worker.call('bus.publish', { channel: 'news', payload: { n: 1 } })).toMatchObject({ ok: true })
    expect(env.worker.posted).toContainEqual({ type: 'bus', channel: 'news', payload: { n: 1 }, from: 'other.tools' })
    expect(other.worker.posted.some((message) => message.type === 'bus')).toBe(false)

    await env.worker.call('bus.unsubscribe', { channel: 'news' })
    await other.worker.call('bus.publish', { channel: 'news', payload: 2 })
    expect(env.worker.posted.filter((message) => message.type === 'bus')).toHaveLength(1)

    for (let index = 0; index < DATA_LIMITS.busSubscriptions; index += 1) await env.worker.call('bus.subscribe', { channel: `c${index}` })
    expect(await env.worker.call('bus.subscribe', { channel: 'c0' })).toMatchObject({ ok: true })
    expect(await env.worker.call('bus.subscribe', { channel: 'one-more' })).toMatchObject({ ok: false, error: { code: 'limit' } })
  })

  it('answers relay hooks and fails open on anything it cannot use', async () => {
    const { worker, control } = await running(DATA)
    expect(await worker.call('relay.register', { hookId: 'h1', phase: 'request', models: ['gpt-*'] })).toMatchObject({ ok: true })
    expect(control.call).toHaveBeenCalledWith('relayhooks.register', { pluginId: 'acme.tools', hookId: 'h1', phase: 'request', models: ['gpt-*'], endpoints: [], timeoutMs: DATA_LIMITS.hookTimeoutMs.default }, expect.any(AbortSignal))
    const invoke = control.listeners.get('relayhooks.invoke')
    const responses = () => control.call.mock.calls.filter(([method]) => method === 'relayhooks.respond').map(([, payload]) => payload)

    invoke?.({ pluginId: 'acme.tools', hookId: 'h1', callId: 'c1', request: { model: 'gpt-4' } })
    expect(worker.posted).toContainEqual({ type: 'hook', kind: 'invoke', hookId: 'h1', callId: 'c1', request: { model: 'gpt-4' } })
    worker.send({ type: 'hookResult', callId: 'c1', decision: { action: 'explode' } })
    worker.send({ type: 'hookResult', callId: 'c1', decision: { action: 'block', block: { status: 403 } } })
    invoke?.({ pluginId: 'acme.tools', hookId: 'h9', callId: 'c2', request: {} })
    invoke?.({ pluginId: 'other.tools', hookId: 'h1', callId: 'c3', request: {} })
    invoke?.({ pluginId: 'acme.tools', hookId: 'h1', callId: 'c4', request: {} })
    worker.send({ type: 'hookResult', callId: 'c4', decision: { action: 'rewrite', model: 'gpt-mini' } })
    expect(responses()).toEqual([
      { callId: 'c1', action: 'continue' },
      { callId: 'c2', action: 'continue' },
      { callId: 'c4', action: 'rewrite', model: 'gpt-mini' },
    ])

    control.listeners.get('relayhooks.changed')?.({ pluginId: 'acme.tools', hookId: 'h1', removed: true, reason: 'timed out' })
    invoke?.({ pluginId: 'acme.tools', hookId: 'h1', callId: 'c5', request: {} })
    expect(responses().at(-1)).toEqual({ callId: 'c5', action: 'continue' })
    // Hooks the relay still calls but this host does not know are orphans:
    // the relay is told to drop them rather than ask again on every request.
    const unregistered = control.call.mock.calls.filter(([method]) => method === 'relayhooks.unregister').map(([, payload]) => payload)
    expect(unregistered).toEqual([{ pluginId: 'acme.tools', hookId: 'h9' }, { pluginId: 'acme.tools', hookId: 'h1' }])
  })

  it('drops a hook whose registration lands after the plugin stopped', async () => {
    const { worker, control, host } = await running(DATA)
    let registered: (value: unknown) => void = () => undefined
    control.call.mockImplementationOnce(() => new Promise((resolve) => { registered = resolve }))
    worker.send({ type: 'call', id: 900, op: 'relay.register', args: { hookId: 'h1', phase: 'request' } })
    await settle()
    host.stop()
    const unregisters = () => control.call.mock.calls.filter(([method]) => method === 'relayhooks.unregister').map(([, payload]) => payload)
    expect(unregisters()).toEqual([{ pluginId: 'acme.tools', hookId: '' }])
    registered(null)
    await settle()
    expect(unregisters()).toEqual([{ pluginId: 'acme.tools', hookId: '' }, { pluginId: 'acme.tools', hookId: 'h1' }])
  })

  it('keeps a hook the restarted worker owns when the old registration lands late', async () => {
    const { worker, control, host, workers } = await running(DATA)
    let registered: (value: unknown) => void = () => undefined
    control.call.mockImplementationOnce(() => new Promise((resolve) => { registered = resolve }))
    worker.send({ type: 'call', id: 900, op: 'relay.register', args: { hookId: 'h1', phase: 'request' } })
    await settle()
    host.stop()
    host.start()
    workers.last.send({ type: 'ready' })
    await workers.last.call('relay.register', { hookId: 'h1', phase: 'request' })
    registered(null)
    await settle()
    const unregisters = control.call.mock.calls.filter(([method]) => method === 'relayhooks.unregister').map(([, payload]) => payload)
    expect(unregisters).toEqual([{ pluginId: 'acme.tools', hookId: '' }])
  })

  it('stamps the plugin as the actor on rule changes only', async () => {
    const { worker, control } = await running(DATA)
    await worker.call('control.call', { method: 'rules.saveRequestRule', payload: { rule: { id: 'r1' }, actor: 'operator' } })
    expect(control.call).toHaveBeenLastCalledWith('rules.saveRequestRule', { rule: { id: 'r1' }, actor: 'plugin:acme.tools' }, expect.any(AbortSignal))
    await worker.call('control.call', { method: 'rules.list', payload: {} })
    expect(control.call).toHaveBeenLastCalledWith('rules.list', {}, expect.any(AbortSignal))
    expect(await worker.call('control.call', { method: 'rules.deleteRetryRule', payload: 'r1' })).toMatchObject({ ok: false, error: { code: 'invalid_argument' } })
    expect(await worker.call('control.call', { method: 'rules.reorder', payload: {} })).toMatchObject({ ok: false, error: { code: 'not_allowed' } })
  })

  it('paints draws on known surfaces and caps tones per second', async () => {
    const { worker, host, sound } = await running(DATA)
    await worker.call('ui.registerWidget', { id: 'load', title: 'Load' })
    const draw = vi.fn()
    host.canvas.subscribe('widget:load', 'c', draw)
    worker.send({ type: 'draw', surface: 'widget:load', canvas: 'c', ops: [{ op: 'clear' }] })
    worker.send({ type: 'draw', surface: 'widget:gone', canvas: 'c', ops: [{ op: 'clear' }] })
    expect(draw).toHaveBeenCalledTimes(1)
    const late = vi.fn()
    host.canvas.subscribe('widget:load', 'c', late)
    expect(late).toHaveBeenCalledWith(draw.mock.calls[0]?.[0])
    // A surface registered again starts blank, not with the old picture.
    await worker.call('ui.unregister', { id: 'widget:load' })
    await worker.call('ui.registerWidget', { id: 'load', title: 'Load' })
    const fresh = vi.fn()
    host.canvas.subscribe('widget:load', 'c', fresh)
    expect(fresh).not.toHaveBeenCalled()

    for (let index = 0; index < DATA_LIMITS.tonesPerSecond + 5; index += 1) worker.send({ type: 'tone', spec: { frequency: 440, durationMs: 50 } })
    expect(sound).toHaveBeenCalledTimes(DATA_LIMITS.tonesPerSecond)
    expect(sound).toHaveBeenCalledWith({ frequency: 440, durationMs: 50, type: 'sine', volume: DATA_LIMITS.toneVolume })

    const { worker: mute, sound: silent } = await running(['ui:slots'])
    mute.send({ type: 'tone', spec: { frequency: 440, durationMs: 50 } })
    expect(silent).not.toHaveBeenCalled()
  })

  it('stops a worker that floods stream messages', async () => {
    const { worker, host } = await running(DATA)
    for (let index = 0; index <= HOST_LIMITS.streamFloodPerSecond; index += 1) worker.send({ type: 'frameDone' })
    expect(host.runtime()).toMatchObject({ reason: expect.stringMatching(/flooding/u) })
    expect(host.runtime().state).not.toBe('running')
  })
})
