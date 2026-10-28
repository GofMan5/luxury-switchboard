import { describe, expect, it, vi } from 'vitest'
import { ExtensionPoints } from '../../../shared/contracts/extensions'
import { EXAMPLE_PLUGINS } from '../domain/examples'
import type { PluginPackage } from '../domain/manifest'
import { settingStorageKey } from '../domain/settings'
import { describe as describeError, PluginsModel, sortUpdates } from './plugins-model'
import { FakeControl, FakeWorkers, MemoryPluginsPort, hostExtras } from './test-fakes'

const pkg: PluginPackage = {
  schema: 1, id: 'acme.tools', name: 'Acme tools', version: '1.0.0',
  permissions: ['ui:pages', 'control:read', 'control:sensitive'], script: 'switchboard.log("hi")',
}

function setup(safeMode = false) {
  const port = new MemoryPluginsPort()
  const workers = new FakeWorkers()
  const extensions = new ExtensionPoints()
  const { bus: _bus, ...extras } = hostExtras()
  const control = new FakeControl()
  const clipboard = vi.fn(async (_text: string) => undefined)
  const notify = vi.fn()
  const model = new PluginsModel({
    ...extras, port, control, workers, extensions, safeMode,
    notify, navigate: vi.fn(), isRoute: () => true, clipboard,
    app: { version: '1.0.0', capabilities: ['plugins.manage'] },
  })
  return { port, workers, extensions, model, control, clipboard, notify, files: extras.files }
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
    const { model, port, workers, control } = setup(true)
    await port.install(pkg, ['ui:pages'])
    await model.connect()
    expect(workers.created).toHaveLength(0)
    // The sidecar outlived the reload: hooks the previous window left go.
    expect(control.call).toHaveBeenCalledWith('relayhooks.reset', {})
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

  it('restarts a crashed plugin on a backoff ladder, then gives up', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] })
    // vi.waitFor advances fake timers on every poll, which would skip the
    // backoff under test; this only drains pending microtasks.
    const settle = async (check: () => void) => {
      for (let turn = 0; ; turn++) {
        try {
          return check()
        } catch (error) {
          if (turn > 100) throw error
          await vi.advanceTimersByTimeAsync(0)
        }
      }
    }
    try {
      const { model, workers } = setup()
      await model.connect()
      await model.install(pkg, ['ui:pages'])
      for (const [attempt, delay] of [[2, 2_000], [3, 10_000], [4, 60_000]] as const) {
        workers.last.send({ type: 'not-a-message' })
        await settle(() => expect(model.snapshot().runtimes['acme.tools'].reason).toContain('Restarting automatically'))
        await vi.advanceTimersByTimeAsync(delay - 1)
        expect(workers.created).toHaveLength(attempt - 1)
        await vi.advanceTimersByTimeAsync(1)
        await settle(() => expect(workers.created).toHaveLength(attempt))
      }
      workers.last.send({ type: 'not-a-message' })
      await settle(() => expect(model.snapshot().runtimes['acme.tools'].reason).toContain('stays stopped'))
      await vi.advanceTimersByTimeAsync(5 * 60_000)
      expect(workers.created).toHaveLength(4)
      await model.restart('acme.tools')
      expect(workers.created).toHaveLength(5)
      workers.last.send({ type: 'not-a-message' })
      await settle(() => expect(model.snapshot().runtimes['acme.tools'].reason).toContain('Restarting automatically'))
    } finally {
      vi.useRealTimers()
    }
  })

  it('does not auto-restart a plugin the operator disabled after it crashed', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] })
    // vi.waitFor advances fake timers on every poll, which would skip the
    // backoff under test; this only drains pending microtasks.
    const settle = async (check: () => void) => {
      for (let turn = 0; ; turn++) {
        try {
          return check()
        } catch (error) {
          if (turn > 100) throw error
          await vi.advanceTimersByTimeAsync(0)
        }
      }
    }
    try {
      const { model, workers } = setup()
      await model.connect()
      await model.install(pkg, ['ui:pages'])
      workers.last.send({ type: 'not-a-message' })
      await settle(() => expect(model.snapshot().runtimes['acme.tools'].state).toBe('crashed'))
      await model.setEnabled('acme.tools', false)
      await vi.advanceTimersByTimeAsync(60_000)
      expect(workers.created).toHaveLength(1)
      expect(model.snapshot().runtimes['acme.tools'].state).toBe('disabled')
    } finally {
      vi.useRealTimers()
    }
  })

  it('leaves a host started by a grant change alone when the old crash timer fires', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] })
    try {
      const { model, workers } = setup()
      await model.connect()
      await model.install(pkg, ['ui:pages'])
      workers.last.send({ type: 'not-a-message' })
      await vi.waitFor(() => expect(model.snapshot().runtimes['acme.tools'].state).toBe('crashed'))
      await model.setGrants('acme.tools', ['ui:pages', 'control:read'])
      expect(workers.created).toHaveLength(2)
      workers.last.send({ type: 'ready' })
      await vi.advanceTimersByTimeAsync(2_000)
      expect(workers.created).toHaveLength(2)
      expect(model.snapshot().runtimes['acme.tools'].state).not.toBe('crashed')
    } finally {
      vi.useRealTimers()
    }
  })

  it('restarts only the crashed plugin the operator picked', async () => {
    const { model, workers } = setup()
    await model.connect()
    await model.install(pkg, ['ui:pages'])
    await model.install({ ...pkg, id: 'acme.other', name: 'Acme other' }, ['ui:pages'])
    for (const worker of workers.created) worker.send({ type: 'not-a-message' })
    await vi.waitFor(() => expect(model.snapshot().runtimes['acme.other'].state).toBe('crashed'))
    await model.restart('acme.tools')
    expect(workers.created.slice(2).map((worker) => worker.context.plugin.id)).toEqual(['acme.tools'])
    expect(model.snapshot().runtimes['acme.other'].state).toBe('crashed')
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
    expect(workers.last.posted).toContainEqual({ type: 'settings', values: { limit: 42, mode: 'fast' }, secrets: [] })

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

  it('publishes slot surfaces and dialogs and routes the operator back to the right plugin', async () => {
    const { model, workers } = setup()
    await model.connect()
    await model.install({ ...pkg, permissions: ['ui:pages', 'ui:slots', 'ui:dialogs'] }, ['ui:pages', 'ui:slots', 'ui:dialogs'])
    const worker = workers.last
    worker.send({ type: 'ready' })
    await worker.call('ui.registerWidget', { id: 'load', title: 'Load' })
    await Promise.resolve()
    expect(model.snapshot().surfaces['acme.tools/widget:load']).toMatchObject({ pluginId: 'acme.tools', surface: 'widget:load' })

    model.sendSurfaceEvent('acme.tools', 'widget:load', { kind: 'action', action: 'go', form: {} })
    expect(worker.posted).toContainEqual({ type: 'ui', pageId: 'widget:load', event: { kind: 'action', action: 'go', form: {} } })
    expect(await worker.call('ui.dialog', { title: 'Hi', tree: { type: 'divider' }, actions: [{ id: 'ok', label: 'OK' }] })).toMatchObject({ ok: true })
    await Promise.resolve()
    expect(model.snapshot().dialogs['acme.tools']).toMatchObject({ id: 'd1', title: 'Hi' })
    model.answerDialog('acme.tools', 'd1', 'ok', {})
    await Promise.resolve()
    expect(model.snapshot().dialogs).toEqual({})
    expect(worker.posted).toContainEqual({ type: 'dialog', id: 'd1', result: { action: 'ok', form: {} } })
  })

  it('stores a secret without keeping it and tells the running plugin which keys are set', async () => {
    const { model, port, workers } = setup()
    await model.connect()
    await model.install(withSettings, withSettings.permissions)
    workers.last.send({ type: 'ready' })
    expect(await model.secretSet('acme.tools', 'token', 'line\nbreak')).toMatchObject({ ok: false })
    expect(await model.secretSet('acme.tools', 'token', 'sk-fictional')).toEqual({ ok: true })
    expect(port.secretWrites).toEqual([{ id: 'acme.tools', key: 'token', set: true }])
    expect(model.snapshot().plugins[0]?.secrets).toEqual(['token'])
    expect(workers.last.posted.at(-1)).toEqual({ type: 'settings', values: { limit: 10, mode: 'fast' }, secrets: ['token'] })
    expect(JSON.stringify(model.snapshot())).not.toContain('sk-fictional')
  })

  it('links a plugin folder and unlinks it', async () => {
    const { model, port } = setup()
    await model.connect()
    expect(await model.devLink('   ')).toEqual({ ok: false, message: 'Enter the folder that holds plugin.json' })
    expect(await model.devLink(' C:\\dev\\plugin ')).toMatchObject({ ok: true, plugin: { id: 'dev.local', dev: { path: 'C:\\dev\\plugin' } } })
    expect(model.snapshot().plugins.find((plugin) => plugin.id === 'dev.local')?.dev).toBeDefined()
    expect(await model.devUnlink('dev.local')).toBe(true)
    expect(model.snapshot().plugins.find((plugin) => plugin.id === 'dev.local')?.dev).toBeUndefined()
    port.failNext = Object.assign(new Error('x'), { code: 'plugin_not_linked' })
    expect(await model.devUnlink('dev.local')).toBe(false)
    expect(model.snapshot().error).toBe('That plugin is not loaded from a folder')
  })
})

describe('PluginsModel.download', () => {
  it('checks a downloaded package before it reaches the review', async () => {
    const { model, port } = setup()
    await model.connect()
    expect(await model.download('  ')).toEqual({ ok: false, message: 'Paste a link to a plugin' })
    expect(await model.download('https://example.com/none.json')).toEqual({ ok: false, message: 'Nothing was found at that link.' })
    port.downloads['https://example.com/bad.json'] = [1]
    expect(await model.download('https://example.com/bad.json')).toEqual({ ok: false, message: 'The link does not hold a valid plugin package', problems: ['A plugin package is a JSON object.'] })
    const pkg = { schema: 1, id: 'acme.dusk', name: 'Dusk', version: '1.0.0', permissions: ['ui:css'], css: ':root{}' }
    port.downloads['https://example.com/dusk.json'] = pkg
    expect(await model.download(' https://example.com/dusk.json ')).toEqual({ ok: true, pkg, source: 'https://example.com/dusk.json' })
    expect(port.plugins).toEqual([])
    expect(model.snapshot().pending).toBe('')
  })
})

describe('PluginsModel.checkUpdates', () => {
  const link = (name: string) => `https://example.com/${name}.json`
  const dusk = { schema: 1, id: 'acme.dusk', name: 'Dusk', version: '1.0.0', permissions: ['ui:css'], css: ':root{}' } as const

  it('offers only a newer version of the same plugin, and drops the offer once installed', async () => {
    const { model, port } = setup()
    await model.connect()
    await model.install(dusk, [], link('dusk'))
    await model.install({ ...dusk, id: 'acme.same' }, [], link('same'))
    await model.install({ ...dusk, id: 'acme.other' }, [], link('other'))
    await model.install({ ...dusk, id: 'acme.gone' }, [], link('gone'))
    await model.install({ ...dusk, id: 'acme.file' }, [])
    port.downloads[link('dusk')] = { ...dusk, version: '1.1.0' }
    port.downloads[link('same')] = { ...dusk, id: 'acme.same', version: '1.0.0+build.2' }
    port.downloads[link('other')] = { ...dusk, id: 'acme.swapped', version: '9.0.0' }
    const calls = vi.spyOn(port, 'download')

    expect(await model.checkUpdates()).toEqual({ newer: 1, changed: 0, failed: 2, checked: 4 })
    expect(calls.mock.calls.map(([url]) => url)).toEqual([link('dusk'), link('same'), link('other'), link('gone')])
    const { updates, checking } = model.snapshot()
    expect(checking).toBe(false)
    expect(updates['acme.dusk']).toEqual({ state: 'newer', pkg: { ...dusk, version: '1.1.0' }, source: link('dusk') })
    expect(updates['acme.same']).toEqual({ state: 'current' })
    expect(updates['acme.other']).toEqual({ state: 'error', message: 'The link now serves a different plugin (acme.swapped)' })
    expect(updates['acme.gone']).toEqual({ state: 'error', message: 'Nothing was found at that link.' })
    expect(updates['acme.file']).toBeUndefined()
    expect(port.plugins.find((plugin) => plugin.id === 'acme.dusk')?.version).toBe('1.0.0')

    await model.install({ ...dusk, version: '1.1.0' }, [], link('dusk'))
    expect(model.snapshot().updates['acme.dusk']).toBeUndefined()
    await model.remove('acme.other')
    expect(model.snapshot().updates['acme.other']).toBeUndefined()
    await model.install({ ...dusk, id: 'acme.gone' }, [], link('moved'))
    expect(model.snapshot().updates['acme.gone']).toBeUndefined()
  })

  it('reports an older, invalid or unordered link as no offer', async () => {
    const { model, port } = setup()
    await model.connect()
    await model.install({ ...dusk, version: '2.0.0' }, [], link('old'))
    await model.install({ ...dusk, id: 'acme.bad' }, [], link('bad'))
    await model.install({ ...dusk, id: 'acme.nightly', version: 'nightly' }, [], link('nightly'))
    port.downloads[link('old')] = { ...dusk, version: '1.9.0' }
    port.downloads[link('bad')] = [1]
    port.downloads[link('nightly')] = { ...dusk, id: 'acme.nightly', version: 'nightly2' }
    expect(await model.checkUpdates()).toEqual({ newer: 0, changed: 0, failed: 2, checked: 3 })
    expect(model.snapshot().updates).toEqual({
      'acme.dusk': { state: 'current' },
      'acme.bad': { state: 'error', message: 'The link no longer holds a valid plugin package' },
      'acme.nightly': { state: 'error', message: 'Version nightly2 cannot be compared with nightly' },
    })
  })

  it('runs one check at a time and does nothing without linked plugins', async () => {
    const { model, port } = setup()
    await model.connect()
    expect(await model.checkUpdates()).toEqual({ newer: 0, changed: 0, failed: 0, checked: 0 })
    await model.install(dusk, [], link('dusk'))
    port.downloads[link('dusk')] = dusk
    const first = model.checkUpdates()
    expect(model.snapshot().checking).toBe(true)
    expect(await model.checkUpdates()).toEqual({ newer: 0, changed: 0, failed: 0, checked: 0 })
    expect(await first).toEqual({ newer: 0, changed: 0, failed: 0, checked: 1 })
    expect(model.snapshot().checking).toBe(false)
  })

  it('flags changed code under the installed version for review, never for Update all', async () => {
    const { model, port } = setup()
    await model.connect()
    await model.install(dusk, [], link('dusk'))
    await model.install({ ...dusk, id: 'acme.same' }, [], link('same'))
    port.downloads[link('dusk')] = { ...dusk, css: ':root{--x:1}' }
    // Key order is not a change.
    port.downloads[link('same')] = { css: ':root{}', permissions: ['ui:css'], version: '1.0.0', name: 'Dusk', id: 'acme.same', schema: 1 }

    expect(await model.checkUpdates()).toEqual({ newer: 0, changed: 1, failed: 0, checked: 2 })
    const { updates, plugins } = model.snapshot()
    expect(updates['acme.same']).toEqual({ state: 'current' })
    expect(updates['acme.dusk']).toEqual({ state: 'changed', pkg: { ...dusk, css: ':root{--x:1}' }, source: link('dusk'), updatedAt: plugins.find((plugin) => plugin.id === 'acme.dusk')?.updatedAt })
    expect(sortUpdates(plugins, updates, '1.0.70')).toEqual({ quiet: [], review: ['acme.dusk'] })
    expect((await model.updateAll('1.0.70')).updated).toEqual([])

    // Reinstalling the reviewed code retires the flag.
    await model.install({ ...dusk, css: ':root{--x:1}' }, [], link('dusk'))
    expect(model.snapshot().updates['acme.dusk']).toBeUndefined()
  })

  it('reports an installed package it cannot read instead of guessing', async () => {
    const { model, port } = setup()
    await model.connect()
    await model.install(dusk, [], link('dusk'))
    port.downloads[link('dusk')] = dusk
    vi.spyOn(port, 'export').mockResolvedValueOnce({ broken: true })
    expect(await model.checkUpdates()).toEqual({ newer: 0, changed: 0, failed: 1, checked: 1 })
    expect(model.snapshot().updates['acme.dusk']).toEqual({ state: 'error', message: 'The installed package could not be read to compare it with the link' })
  })
})

describe('PluginsModel.announceUpdates', () => {
  const link = (name: string) => `https://example.com/${name}.json`
  const dusk = { schema: 1, id: 'acme.dusk', name: 'Dusk', version: '1.0.0', permissions: ['ui:css'], css: ':root{}' } as const

  it('announces newer versions without installing them, and stays quiet otherwise', async () => {
    const { model, port, notify } = setup()
    await model.connect()
    await model.announceUpdates()
    await model.install(dusk, [], link('dusk'))
    await model.install({ ...dusk, id: 'acme.dawn' }, [], link('dawn'))
    port.downloads[link('dusk')] = dusk
    await model.announceUpdates()
    expect(notify).not.toHaveBeenCalled()

    port.downloads[link('dusk')] = { ...dusk, version: '1.1.0' }
    await model.announceUpdates()
    expect(notify).toHaveBeenLastCalledWith({ kind: 'plugin', severity: 'info', title: 'A plugin update is available', body: 'Open Plugins to review the changes before updating.' })
    port.downloads[link('dawn')] = { ...dusk, id: 'acme.dawn', version: '2.0.0' }
    await model.announceUpdates()
    expect(notify).toHaveBeenLastCalledWith(expect.objectContaining({ title: '2 plugin updates are available' }))
    expect(port.plugins.map((plugin) => plugin.version)).toEqual(['1.0.0', '1.0.0'])

    port.downloads[link('dawn')] = { ...dusk, id: 'acme.dawn', css: ':root{--y:2}' }
    await model.announceUpdates()
    expect(notify).toHaveBeenLastCalledWith(expect.objectContaining({
      title: '2 plugin updates are available',
      body: 'Open Plugins to review the changes before updating. Some links serve changed code under the installed version number.',
    }))
  })
})

describe('PluginsModel.updateAll', () => {
  const link = (name: string) => `https://example.com/${name}.json`
  const dusk = { schema: 1, id: 'acme.dusk', name: 'Dusk', version: '1.0.0', permissions: ['ui:css', 'ui:pages'], css: ':root{}', script: 'ready()' } as const

  it('installs quiet updates with the previous decisions and leaves the rest for review', async () => {
    const { model, port } = setup()
    await model.connect()
    await model.install(dusk, ['ui:css'], link('dusk'))
    await model.install({ ...dusk, id: 'acme.more', permissions: ['ui:css'] }, ['ui:css'], link('more'))
    await model.install({ ...dusk, id: 'acme.newer' }, ['ui:css'], link('newer'))
    await model.install({ ...dusk, id: 'acme.off' }, ['ui:css'], link('off'))
    await model.setEnabled('acme.off', false)
    await model.install({ ...dusk, id: 'acme.same' }, ['ui:css'], link('same'))
    port.downloads[link('dusk')] = { ...dusk, version: '1.1.0' }
    port.downloads[link('more')] = { ...dusk, id: 'acme.more', version: '1.1.0' }
    port.downloads[link('newer')] = { ...dusk, id: 'acme.newer', version: '1.1.0', minAppVersion: '9.0.0' }
    port.downloads[link('off')] = { ...dusk, id: 'acme.off', version: '1.1.0' }
    port.downloads[link('same')] = { ...dusk, id: 'acme.same' }
    expect(await model.checkUpdates()).toMatchObject({ newer: 4 })
    const installs = vi.spyOn(port, 'install')

    const result = await model.updateAll('1.0.70')
    expect(result.updated.map((plugin) => plugin.id)).toEqual(['acme.dusk'])
    expect(result.review).toEqual(['acme.more', 'acme.newer', 'acme.off'])
    expect(result.failed).toEqual([])
    // The previous version's refusal of ui:pages stands; nothing new is granted.
    expect(installs).toHaveBeenCalledTimes(1)
    expect(installs).toHaveBeenCalledWith({ ...dusk, version: '1.1.0' }, ['ui:css'], link('dusk'))
    const state = model.snapshot()
    expect(state.pending).toBe('')
    expect(Object.keys(state.updates).filter((id) => state.updates[id]?.state === 'newer')).toEqual(['acme.more', 'acme.newer', 'acme.off'])
    expect(state.plugins.find((plugin) => plugin.id === 'acme.off')?.enabled).toBe(false)
  })

  it('reports a failed install and does nothing while another change runs or without offers', async () => {
    const { model, port } = setup()
    await model.connect()
    expect(await model.updateAll('1.0.70')).toEqual({ updated: [], review: [], failed: [] })
    await model.install(dusk, ['ui:css'], link('dusk'))
    port.downloads[link('dusk')] = { ...dusk, version: '1.1.0' }
    await model.checkUpdates()
    vi.spyOn(port, 'install').mockRejectedValueOnce(new Error('Disk is full'))
    expect(await model.updateAll('1.0.70')).toEqual({ updated: [], review: [], failed: [{ id: 'acme.dusk', message: 'Disk is full' }] })
    expect(model.snapshot().updates['acme.dusk']?.state).toBe('newer')

    const running = model.updateAll('1.0.70')
    expect(model.snapshot().pending).toBe('update-all')
    expect(await model.install(dusk, [], link('dusk'))).toEqual({ ok: false, message: 'Another plugin change is still running' })
    expect(await model.updateAll('1.0.70')).toEqual({ updated: [], review: [], failed: [] })
    expect((await running).updated).toHaveLength(1)
  })
})

describe('describe', () => {
  it('turns control-plane codes into sentences', () => {
    expect(describeError(Object.assign(new Error('x'), { code: 'plugin_limit' }))).toBe('At most 24 plugins can be installed; remove one first')
    expect(describeError(new Error('Exact words'))).toBe('Exact words')
    expect(describeError('nope')).toBe('The plugin change failed')
  })
})

describe('PluginsModel stored data', () => {
  it('counts the plugin own values apart from settings and reports collections', async () => {
    const { model, port } = setup()
    await model.connect()
    await model.install(withSettings, ['ui:commands'])
    port.values.set('acme.tools', { [settingStorageKey('limit')]: '42', cursor: 'é' })
    port.collections.set('acme.tools', { notes: { count: 3, bytes: 120 } })

    const result = await model.loadData('acme.tools')
    expect(result).toEqual({
      ok: true,
      usage: {
        storage: { values: 1, bytes: settingStorageKey('limit').length + 2 + 'cursor'.length + 2, limitBytes: 64 * 1024, limitKeys: 64 },
        collections: { collections: [{ name: 'notes', count: 3, bytes: 120 }], bytes: 120, limitBytes: 2 * 1024 * 1024 },
      },
    })
  })

  it('clears values and collections, keeps settings and restarts the running plugin', async () => {
    const { model, port, workers } = setup()
    await model.connect()
    await model.install(withSettings, ['ui:commands'])
    const first = workers.last
    port.values.set('acme.tools', { [settingStorageKey('limit')]: '42', cursor: 'x', seen: 'y' })
    port.collections.set('acme.tools', { notes: { count: 3, bytes: 120 } })

    const result = await model.clearData('acme.tools')
    expect(result).toMatchObject({ ok: true, usage: { storage: { values: 0 }, collections: { collections: [], bytes: 0 } } })
    expect(port.values.get('acme.tools')).toEqual({ [settingStorageKey('limit')]: '42' })
    expect(port.collections.has('acme.tools')).toBe(false)
    expect(first.terminated).toBe(true)
    expect(workers.created).toHaveLength(2)
    expect(model.snapshot().pending).toBe('')
  })

  it('lets a write the plugin already made land before clearing', async () => {
    const { model, port, workers } = setup()
    await model.connect()
    await model.install({ ...withSettings, permissions: [...withSettings.permissions, 'storage'] }, ['ui:commands', 'storage'])
    let release = (): void => undefined
    const gate = new Promise<void>((resolve) => { release = resolve })
    const original = port.storageUpdate.bind(port)
    const update = vi.spyOn(port, 'storageUpdate').mockImplementationOnce(async (id, changes) => { await gate; return original(id, changes) })
    workers.last.send({ type: 'call', id: 1, op: 'storage.set', args: { key: 'late', value: 'x' } })
    await vi.waitFor(() => expect(update).toHaveBeenCalled())

    const clearing = model.clearData('acme.tools')
    await new Promise((resolve) => setTimeout(resolve, 20))
    release()
    expect(await clearing).toMatchObject({ ok: true })
    expect(port.values.get('acme.tools') ?? {}).toEqual({})
  })

  it('skips the storage write when only settings are stored', async () => {
    const { model, port } = setup()
    await model.connect()
    await model.install(withSettings, ['ui:commands'])
    port.values.set('acme.tools', { [settingStorageKey('limit')]: '42' })
    const update = vi.spyOn(port, 'storageUpdate')
    expect(await model.clearData('acme.tools')).toMatchObject({ ok: true })
    expect(update).not.toHaveBeenCalled()
  })

  it('surfaces a failure and refuses while another change runs', async () => {
    const { model, port } = setup()
    await model.connect()
    await model.install(withSettings, ['ui:commands'])
    port.failNext = new Error('Storage is unavailable')
    expect(await model.clearData('acme.tools')).toEqual({ ok: false, message: 'Storage is unavailable' })
    expect(model.snapshot().pending).toBe('')

    port.failNext = new Error('Storage is unavailable')
    expect(await model.loadData('acme.tools')).toEqual({ ok: false, message: 'Storage is unavailable' })

    const removing = model.remove('acme.tools')
    expect(await model.clearData('acme.tools')).toMatchObject({ ok: false })
    await removing
  })
})

describe('PluginsModel log export', () => {
  const lines = [{ at: Date.UTC(2026, 0, 2), level: 'warn' as const, text: 'slow answer' }]

  it('copies and saves the given lines as text', async () => {
    const { model, clipboard, files } = setup()
    expect(await model.copyLog(lines)).toBe(true)
    expect(clipboard).toHaveBeenCalledWith('2026-01-02T00:00:00.000Z WARN  slow answer\n')
    expect(await model.saveLog('acme.tools', lines)).toBe(true)
    expect(files.saved).toEqual([{ name: 'acme.tools-log.txt', text: '2026-01-02T00:00:00.000Z WARN  slow answer\n' }])
  })

  it('reports a refused clipboard or failed save, and a cancelled save quietly', async () => {
    const { model, clipboard, files } = setup()
    clipboard.mockRejectedValueOnce(new Error('Clipboard access denied'))
    expect(await model.copyLog(lines)).toBe(false)
    expect(model.snapshot().error).toBe('Could not copy the log: Clipboard access denied')
    files.saveAnswer = false
    expect(await model.saveLog('acme.tools', lines)).toBe(false)
    vi.spyOn(files, 'save').mockRejectedValueOnce(new Error('Disk full'))
    expect(await model.saveLog('acme.tools', lines)).toBe(false)
    expect(model.snapshot().error).toBe('Could not save the log: Disk full')
  })
})
