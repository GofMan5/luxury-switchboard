import { describe, expect, it, vi } from 'vitest'
import { ExtensionPoints } from '../../../shared/contracts/extensions'
import { EXAMPLE_PLUGINS } from '../domain/examples'
import type { PluginPackage } from '../domain/manifest'
import { describe as describeError, PluginsModel } from './plugins-model'
import { FakeControl, FakeWorkers, MemoryPluginsPort } from './test-fakes'

const pkg: PluginPackage = {
  schema: 1, id: 'acme.tools', name: 'Acme tools', version: '1.0.0',
  permissions: ['ui:pages', 'control:read', 'control:sensitive'], script: 'switchboard.log("hi")',
}

function setup(safeMode = false) {
  const port = new MemoryPluginsPort()
  const workers = new FakeWorkers()
  const extensions = new ExtensionPoints()
  const model = new PluginsModel({
    port, control: new FakeControl(), workers, extensions, safeMode,
    notify: vi.fn(), navigate: vi.fn(), isRoute: () => true, clipboard: vi.fn(async () => undefined),
    app: { version: '1.0.0', capabilities: ['plugins.manage'] },
  })
  return { port, workers, extensions, model }
}

const withSettings: PluginPackage = {
  ...pkg,
  permissions: ['ui:commands', 'ui:notify'],
  settings: [
    { key: 'limit', label: 'Limit', description: '', type: 'number', default: 10, placeholder: '', min: 1, max: 100 },
    { key: 'mode', label: 'Mode', description: '', type: 'select', default: 'fast', options: [{ value: 'fast', label: 'Fast' }, { value: 'safe', label: 'Safe' }] },
  ],
}

describe('PluginsModel', () => {
  it('installs with the ticked grants trimmed to what the package declares, and starts it', async () => {
    const { model, port, workers } = setup()
    await model.connect()
    expect(model.snapshot().phase).toBe('ready')

    const result = await model.install(pkg, ['ui:pages', 'control:read', 'control:write'])
    expect(result).toMatchObject({ ok: true, plugin: { id: 'acme.tools', granted: ['ui:pages', 'control:read'] } })
    expect(port.plugins[0].granted).toEqual(['ui:pages', 'control:read'])
    expect(workers.created).toHaveLength(1)
    expect(workers.last.context.plugin.granted).toEqual(['ui:pages', 'control:read'])
    expect(model.snapshot()).toMatchObject({ pending: '', runtimes: { 'acme.tools': { state: 'starting' } } })
  })

  it('refuses a package over the shell budget before sending it', async () => {
    const { model, port } = setup()
    await model.connect()
    const install = vi.spyOn(port, 'install')
    const result = await model.install({ ...pkg, script: '\\'.repeat(128 * 1024) }, ['ui:pages'])
    expect(result).toMatchObject({ ok: false, message: expect.stringContaining('256 KiB') })
    expect(install).not.toHaveBeenCalled()
  })

  it('stops a disabled plugin, starts it again when enabled and stops it when removed', async () => {
    const { model, port, workers } = setup()
    await model.connect()
    await model.install(pkg, ['ui:pages'])
    const first = workers.last

    expect(await model.setEnabled('acme.tools', false)).toBe(true)
    expect(first.terminated).toBe(true)
    expect(model.snapshot().runtimes['acme.tools'].state).toBe('disabled')

    await model.setEnabled('acme.tools', true)
    expect(workers.created).toHaveLength(2)

    expect(await model.remove('acme.tools')).toBe(true)
    expect(workers.last.terminated).toBe(true)
    expect(port.plugins).toEqual([])
    expect(model.snapshot().plugins).toEqual([])
  })

  it('restarts a plugin whose grants change, and leaves it alone for unrelated updates', async () => {
    const { model, port, workers } = setup()
    await model.connect()
    await model.install(pkg, ['ui:pages'])
    port.emit()
    await model.refresh()
    expect(workers.created).toHaveLength(1)

    await model.setGrants('acme.tools', ['ui:pages', 'control:sensitive'])
    expect(workers.created).toHaveLength(2)
    expect(workers.last.context.plugin.granted).toEqual(['ui:pages', 'control:sensitive'])
  })

  it('follows changes made in another window', async () => {
    const { model, port, workers } = setup()
    await model.connect()
    await port.install(pkg, ['ui:pages'])
    port.emit()
    await vi.waitFor(() => expect(workers.created).toHaveLength(1))
    expect(model.snapshot().plugins.map((plugin) => plugin.id)).toEqual(['acme.tools'])
  })

  it('lists but never runs plugins in safe mode', async () => {
    const { model, port, workers } = setup(true)
    await port.install(pkg, ['ui:pages'])
    await model.connect()
    expect(workers.created).toHaveLength(0)
    expect(model.snapshot()).toMatchObject({ safeMode: true, runtimes: { 'acme.tools': { state: 'stopped', reason: expect.stringContaining('Safe mode') } } })
  })

  it('restarts a crashed plugin on request', async () => {
    const { model, workers } = setup()
    await model.connect()
    await model.install(pkg, ['ui:pages'])
    workers.last.send({ type: 'not-a-message' })
    await vi.waitFor(() => expect(model.snapshot().runtimes['acme.tools'].state).toBe('crashed'))
    await model.refresh()
    expect(workers.created).toHaveLength(1)
    await model.restart('acme.tools')
    expect(workers.created).toHaveLength(2)
  })

  it('reports a plugin it could not load as crashed with the reason', async () => {
    const { model, port } = setup()
    await port.install(pkg, ['ui:pages'])
    vi.spyOn(port, 'get').mockRejectedValueOnce(Object.assign(new Error('gone'), { code: 'plugin_not_found' }))
    await model.connect()
    expect(model.snapshot().runtimes['acme.tools']).toMatchObject({ state: 'crashed', reason: 'The plugin could not be loaded: That plugin is no longer installed.' })
  })

  it('shows an error when the store is unavailable, and keeps a loaded list on later failures', async () => {
    const { model, port } = setup()
    port.failNext = new Error('down')
    await model.connect()
    expect(model.snapshot()).toMatchObject({ phase: 'error', error: 'Plugins are unavailable' })
    await model.refresh()
    expect(model.snapshot()).toMatchObject({ phase: 'ready', error: '' })
    port.failNext = new Error('down')
    await model.refresh()
    expect(model.snapshot().phase).toBe('ready')
  })

  it('surfaces install failures in words', async () => {
    const { model, port } = setup()
    await model.connect()
    port.failNext = Object.assign(new Error('Plugin "script" is too large'), { code: 'invalid_plugin' })
    expect(await model.install(pkg, [])).toEqual({ ok: false, message: 'The control plane refused this package: "script" is too large' })
  })

  it('runs the bundled examples', async () => {
    const { model, workers, extensions } = setup()
    await model.connect()
    for (const example of EXAMPLE_PLUGINS) expect(await model.install(example.package, example.package.permissions), example.file).toMatchObject({ ok: true })
    const scripted = EXAMPLE_PLUGINS.filter((example) => example.package.script)
    expect(workers.created).toHaveLength(scripted.length)
    expect(extensions.snapshot().themes.length).toBeGreaterThan(0)
  })

  it('saves settings in one write, drops values equal to the default, and pushes them to the running plugin', async () => {
    const { model, port, workers } = setup()
    await model.connect()
    await model.install(withSettings, withSettings.permissions)
    expect(workers.last.context.settings).toEqual({ limit: 10, mode: 'fast' })
    workers.last.send({ type: 'ready' })
    const view = await model.loadSettings('acme.tools')
    expect(view.values).toEqual({ limit: 10, mode: 'fast' })

    expect(await model.saveSettings('acme.tools', view.settings, { limit: 500 })).toEqual({ ok: false, message: 'Limit: Enter a number from 1 to 100.' })
    expect(port.writes).toHaveLength(0)
    expect(await model.saveSettings('acme.tools', view.settings, { limit: 42, mode: 'fast' })).toEqual({ ok: true })
    expect(port.writes).toEqual([{ 'setting:limit': '42', 'setting:mode': null }])
    expect(workers.last.posted).toContainEqual({ type: 'settings', values: { limit: 42, mode: 'fast' } })

    // A restart boots with the saved values, never the defaults first.
    await model.restart('acme.tools')
    expect(workers.last.context.settings).toEqual({ limit: 42, mode: 'fast' })
  })

  it('queues plugin questions and answers no for a plugin that stops', async () => {
    const { model, workers, extensions } = setup()
    await model.connect()
    await model.install(withSettings, withSettings.permissions)
    const worker = workers.last
    worker.send({ type: 'ready' })
    await worker.call('commands.register', { id: 'go', title: 'Go' })
    await extensions.snapshot().commands[0].run()
    worker.send({ type: 'call', id: 40, op: 'ui.confirm', args: { title: 'Proceed?' } })
    await vi.waitFor(() => expect(model.snapshot().asking).toMatchObject({ pluginName: 'Acme tools', title: 'Proceed?', confirmText: 'Continue', tone: 'neutral' }))
    model.answer(model.snapshot().asking?.id ?? 0, true)
    await vi.waitFor(() => expect(worker.posted).toContainEqual({ type: 'result', id: 40, ok: true, value: true }))
    expect(model.snapshot().asking).toBeNull()

    worker.send({ type: 'call', id: 41, op: 'ui.confirm', args: { title: 'Again?' } })
    await vi.waitFor(() => expect(model.snapshot().asking).not.toBeNull())
    await model.setEnabled('acme.tools', false)
    expect(model.snapshot().asking).toBeNull()
  })

  it('reloads a plugin when its package changes even within the same timestamp', async () => {
    const { model, port, workers } = setup()
    await model.connect()
    await model.install(pkg, ['ui:pages'])
    port.plugins[0] = { ...port.plugins[0], version: '1.0.1' }
    await model.refresh()
    expect(workers.created).toHaveLength(2)
  })

  it('stops every plugin and takes back their contributions on dispose', async () => {
    const { model, workers, extensions } = setup()
    await model.connect()
    await model.install({ ...pkg, permissions: ['ui:css', 'ui:pages'], css: 'body{}' }, ['ui:css', 'ui:pages'])
    expect(extensions.snapshot().styles).toHaveLength(1)
    model.dispose()
    expect(workers.last.terminated).toBe(true)
    expect(extensions.snapshot().styles).toEqual([])
  })
})

describe('describe', () => {
  it('turns control-plane codes into sentences', () => {
    expect(describeError(Object.assign(new Error('x'), { code: 'plugin_limit' }))).toBe('At most 24 plugins can be installed; remove one first')
    expect(describeError(new Error('Exact words'))).toBe('Exact words')
    expect(describeError('nope')).toBe('The plugin change failed')
  })
})
