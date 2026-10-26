import { describe, expect, it, vi } from 'vitest'
import type { ControlPlaneSession } from '../../../platform/stdio/session'
import { SessionControlPort, StdioPluginsPort } from './stdio-plugins-port'

const PLUGIN = {
  id: 'acme.tools', name: 'Acme tools', version: '1.0.0', author: 'Acme', description: '',
  permissions: ['ui:pages'], granted: ['ui:pages'], enabled: true,
  installedAt: '2026-01-01T00:00:00Z', updatedAt: '2026-01-01T00:00:00Z', sizeBytes: 64,
  hasScript: true, hasCss: false, hasTheme: false, homepage: 'https://example.com/acme', minAppVersion: '1.0.65',
  hasSettings: true, problem: '', secrets: [], hosts: [], source: 'https://example.test/acme/tools.json',
}

type Listener = (event: { payload: unknown }) => void

function fakeSession(answers: Record<string, unknown>) {
  const listeners = new Map<string, Listener>()
  const call = vi.fn(async (method: string) => {
    if (!(method in answers)) throw Object.assign(new Error(`no ${method}`), { code: 'unknown_command' })
    return answers[method]
  })
  const subscribe = vi.fn((topic: string, listener: Listener) => { listeners.set(topic, listener); return () => { listeners.delete(topic) } })
  const session = { call, subscribe, start: async () => undefined, stop: async () => undefined } as unknown as ControlPlaneSession
  return { session, call, listeners }
}

describe('StdioPluginsPort', () => {
  it('sends the documented commands and normalizes the answers', async () => {
    const { session, call } = fakeSession({
      'plugins.list': { plugins: [PLUGIN] },
      'plugins.get': { plugin: PLUGIN, script: 'x', css: '', theme: null, settings: [{ key: 'limit', label: 'Limit', type: 'number', default: 5 }] },
      'plugins.install': { plugin: PLUGIN },
      'plugins.update': { plugin: { ...PLUGIN, enabled: false } },
      'plugins.remove': { removed: true },
      'plugins.storage.get': { values: { a: '1', bad: 2 } },
      'plugins.storage.update': { values: { a: '2' } },
    })
    const port = new StdioPluginsPort(session)
    const signal = new AbortController().signal

    expect(await port.list(signal)).toEqual([PLUGIN])
    expect(call).toHaveBeenLastCalledWith('plugins.list', {}, signal)
    // Omitted setting attributes are filled with their defaults.
    expect(await port.get('acme.tools')).toEqual({ plugin: PLUGIN, script: 'x', css: '', theme: null, settings: [{ key: 'limit', label: 'Limit', description: '', type: 'number', default: 5, placeholder: '' }] })
    expect(call).toHaveBeenLastCalledWith('plugins.get', { id: 'acme.tools' }, undefined)

    const pkg = { schema: 1, id: 'acme.tools', name: 'Acme tools', version: '1.0.0', permissions: ['ui:pages'] } as const
    expect(await port.install(pkg, ['ui:pages'])).toEqual(PLUGIN)
    expect(call).toHaveBeenLastCalledWith('plugins.install', { package: pkg, granted: ['ui:pages'] })

    expect(await port.update('acme.tools', { enabled: false })).toMatchObject({ enabled: false })
    expect(call).toHaveBeenLastCalledWith('plugins.update', { id: 'acme.tools', enabled: false })

    await port.remove('acme.tools')
    expect(call).toHaveBeenLastCalledWith('plugins.remove', { id: 'acme.tools' })

    expect(await port.storageGet('acme.tools')).toEqual({ a: '1' })
    expect(await port.storageUpdate('acme.tools', { a: '2', b: null })).toEqual({ a: '2' })
    expect(call).toHaveBeenLastCalledWith('plugins.storage.update', { id: 'acme.tools', values: { a: '2', b: null } })
  })

  it('sends the link a package came from and exports the stored package', async () => {
    const pkg = { schema: 1 as const, id: 'acme.tools', name: 'Acme tools', version: '1.0.0', permissions: [] }
    const { session, call } = fakeSession({ 'plugins.install': { plugin: PLUGIN }, 'plugins.export': { package: pkg } })
    const port = new StdioPluginsPort(session)
    await port.install(pkg, [], 'https://example.test/acme/tools.json')
    expect(call).toHaveBeenLastCalledWith('plugins.install', { package: pkg, granted: [], source: 'https://example.test/acme/tools.json' })
    expect(await port.export('acme.tools')).toEqual(pkg)
    expect(call).toHaveBeenLastCalledWith('plugins.export', { id: 'acme.tools' })
    const { session: plain } = fakeSession({ 'plugins.list': { plugins: [{ ...PLUGIN, source: 'http://example.test/x.json' }] } })
    expect((await new StdioPluginsPort(plain).list())[0]?.source).toBe('')
  })

  it('downloads a package without installing it', async () => {
    const { session, call } = fakeSession({ 'plugins.download': { package: { id: 'acme.tools' }, source: 'https://raw.githubusercontent.com/acme/tools/HEAD/' } })
    expect(await new StdioPluginsPort(session).download('github.com/acme/tools')).toEqual({ package: { id: 'acme.tools' }, source: 'https://raw.githubusercontent.com/acme/tools/HEAD/' })
    expect(call).toHaveBeenLastCalledWith('plugins.download', { url: 'github.com/acme/tools' })
    const { session: odd } = fakeSession({ 'plugins.download': { source: 7 } })
    expect(await new StdioPluginsPort(odd).download('x')).toEqual({ package: undefined, source: '' })
  })

  it('refuses an answer without a plugin', async () => {
    const { session } = fakeSession({ 'plugins.install': {} })
    await expect(new StdioPluginsPort(session).install({ schema: 1, id: 'a.b', name: 'x', version: '1', permissions: [] }, [])).rejects.toThrow('without an id')
  })

  it('forwards plugins.changed and ignores malformed events', () => {
    const { session, listeners } = fakeSession({})
    const listener = vi.fn()
    const stop = new StdioPluginsPort(session).subscribe(listener)
    listeners.get('plugins.changed')?.({ payload: { plugins: [PLUGIN] } })
    expect(listener).toHaveBeenCalledWith([PLUGIN])
    listeners.get('plugins.changed')?.({ payload: { plugins: [{ name: 'no id' }] } })
    expect(listener).toHaveBeenCalledTimes(1)
    stop()
    expect(listeners.has('plugins.changed')).toBe(false)
  })
})

describe('SessionControlPort', () => {
  it('carries calls and topic payloads unchanged', async () => {
    const { session, call, listeners } = fakeSession({ 'relay.status': { running: true } })
    const port = new SessionControlPort(session)
    const signal = new AbortController().signal
    expect(await port.call('relay.status', { a: 1 }, signal)).toEqual({ running: true })
    expect(call).toHaveBeenCalledWith('relay.status', { a: 1 }, signal)
    const listener = vi.fn()
    port.subscribe('relay.changed', listener)
    listeners.get('relay.changed')?.({ payload: { running: false } })
    expect(listener).toHaveBeenCalledWith({ running: false })
  })
})
