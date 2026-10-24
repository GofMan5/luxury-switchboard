import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ExtensionPoints } from '../../../shared/contracts/extensions'
import type { PluginPermission } from '../domain/permissions'
import type { PluginDetail } from '../domain/plugin'
import { RUNTIME_LIMITS } from '../domain/protocol'
import { HOST_LIMITS, PluginHost } from './plugin-host'
import { FakeControl, FakeWorkers, MemoryPluginsPort, detail } from './test-fakes'

const ALL: PluginPermission[] = ['ui:theme', 'ui:css', 'ui:pages', 'ui:status', 'ui:commands', 'ui:notify', 'control:read', 'control:write', 'storage']

function setup(grants: readonly PluginPermission[] = ALL, plugin: PluginDetail = detail({ permissions: [...grants], granted: [...grants] })) {
  const workers = new FakeWorkers()
  const control = new FakeControl()
  const storage = new MemoryPluginsPort()
  const extensions = new ExtensionPoints()
  const notify = vi.fn()
  const navigate = vi.fn()
  const onChange = vi.fn()
  const host = new PluginHost(plugin, grants, {
    workers, control, storage, extensions, notify, navigate, onChange,
    isRoute: (route) => route === 'overview' || route === 'plugin/acme.tools/board',
    app: { version: '1.0.0', capabilities: ['plugins.manage'] },
  })
  return { host, workers, control, storage, extensions, notify, navigate, onChange }
}

async function running(grants?: readonly PluginPermission[], plugin?: PluginDetail) {
  const env = setup(grants, plugin)
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

  it('navigates only to routes the shell has', async () => {
    const { worker, navigate } = await running([])
    expect(await worker.call('navigate', { route: '#overview' })).toMatchObject({ ok: true })
    expect(navigate).toHaveBeenCalledWith('overview')
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

  it('delivers page events only for registered pages once running', async () => {
    const { worker, host } = await running()
    host.dispatch('board', { kind: 'show', form: {} })
    expect(worker.posted.filter((message) => message.type === 'ui')).toHaveLength(0)
    await worker.call('ui.registerPage', { id: 'board', title: 'Board' })
    host.dispatch('board', { kind: 'action', action: 'refresh', form: { q: 'x' } })
    expect(worker.posted).toContainEqual({ type: 'ui', pageId: 'board', event: { kind: 'action', action: 'refresh', form: { q: 'x' } } })
  })
})
