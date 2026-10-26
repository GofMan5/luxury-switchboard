import { vi } from 'vitest'
import type { PluginDetail, PluginSummary } from '../domain/plugin'
import type { HostMessage } from '../domain/protocol'
import type { OpenSpec } from '../domain/specs'
import { PluginBus } from './plugin-bus'
import type { BootContext, ControlPort, FilesPort, PickedFile, PluginWorker, PluginsPort, ThemeInfo, ThemePort, WorkerFactory } from './ports'

/** Test doubles for the plugin runtime: a worker the test drives by hand, a
 * control plane that records calls, and an in-memory plugin store. */
export class FakeWorker implements PluginWorker {
  readonly posted: HostMessage[] = []
  terminated = false
  #message: ((data: unknown) => void) | null = null
  #error: ((message: string) => void) | null = null
  readonly script: string
  readonly context: BootContext

  constructor(script: string, context: BootContext) {
    this.script = script
    this.context = context
  }

  post(message: HostMessage): void { this.posted.push(message) }
  onMessage(listener: (data: unknown) => void): void { this.#message = listener }
  onError(listener: (message: string) => void): void { this.#error = listener }
  terminate(): void { this.terminated = true }

  /** What the script would post. */
  send(data: unknown): void { this.#message?.(data) }
  fail(message: string): void { this.#error?.(message) }

  /** Posts a call and waits for the host's answer to it. */
  async call(op: string, args: Record<string, unknown> = {}): Promise<Extract<HostMessage, { type: 'result' }>> {
    const id = this.#nextId++
    this.send({ type: 'call', id, op, args })
    for (let attempt = 0; attempt < 50; attempt += 1) {
      const result = this.posted.find((message): message is Extract<HostMessage, { type: 'result' }> => message.type === 'result' && message.id === id)
      if (result) return result
      await Promise.resolve()
    }
    throw new Error(`No answer to ${op}`)
  }

  #nextId = 1
}

export class FakeWorkers implements WorkerFactory {
  readonly created: FakeWorker[] = []
  create(script: string, context: BootContext): PluginWorker {
    const worker = new FakeWorker(script, context)
    this.created.push(worker)
    return worker
  }
  get last(): FakeWorker {
    const worker = this.created.at(-1)
    if (!worker) throw new Error('No worker was started')
    return worker
  }
}

export class FakeControl implements ControlPort {
  readonly call = vi.fn<ControlPort['call']>(async (method) => ({ method }))
  readonly listeners = new Map<string, (payload: unknown) => void>()
  readonly unsubscribed: string[] = []
  subscribe(topic: string, listener: (payload: unknown) => void): () => void {
    this.listeners.set(topic, listener)
    return () => { this.unsubscribed.push(topic); this.listeners.delete(topic) }
  }
}

export function summary(overrides: Partial<PluginSummary> = {}): PluginSummary {
  return {
    id: 'acme.tools', name: 'Acme tools', version: '1.0.0', author: 'Acme', description: '', homepage: '', minAppVersion: '',
    permissions: ['ui:pages'], granted: ['ui:pages'], enabled: true,
    installedAt: '2026-01-01T00:00:00Z', updatedAt: '2026-01-01T00:00:00Z', sizeBytes: 512,
    hasScript: true, hasCss: false, hasTheme: false, hasSettings: false, problem: '', secrets: [], hosts: [],
    ...overrides,
  }
}

export function detail(overrides: Partial<PluginSummary> = {}, assets: Partial<Omit<PluginDetail, 'plugin'>> = {}): PluginDetail {
  return { plugin: summary({ hasSettings: Boolean(assets.settings?.length), ...overrides }), script: 'switchboard.log("hi")', css: '', theme: null, settings: [], ...assets }
}

/** A plugin store that behaves like the control plane, in memory. */
export class MemoryPluginsPort implements PluginsPort {
  plugins: PluginSummary[] = []
  readonly assets = new Map<string, Omit<PluginDetail, 'plugin'>>()
  readonly values = new Map<string, Record<string, string>>()
  readonly listeners = new Set<(plugins: PluginSummary[]) => void>()
  failNext: Error | null = null

  async list(): Promise<PluginSummary[]> { return this.#guard(() => [...this.plugins]) }
  async get(id: string): Promise<PluginDetail> {
    return this.#guard(() => {
      const plugin = this.plugins.find((entry) => entry.id === id)
      if (!plugin) throw Object.assign(new Error('not found'), { code: 'plugin_not_found' })
      return { plugin, ...(this.assets.get(id) ?? { script: '', css: '', theme: null, settings: [] }) }
    })
  }
  async install(pkg: Parameters<PluginsPort['install']>[0], granted: readonly string[]): Promise<PluginSummary> {
    return this.#guard(() => {
      this.#installs += 1
      const plugin = summary({
        id: pkg.id, name: pkg.name, version: pkg.version, permissions: pkg.permissions, granted: [...granted],
        homepage: pkg.homepage ?? '', minAppVersion: pkg.minAppVersion ?? '',
        updatedAt: `2026-01-01T00:00:${String(this.#installs).padStart(2, '0')}Z`,
        hasScript: Boolean(pkg.script), hasCss: Boolean(pkg.css), hasTheme: Boolean(pkg.theme), hasSettings: Boolean(pkg.settings?.length),
      })
      this.plugins = [...this.plugins.filter((entry) => entry.id !== pkg.id), plugin]
      this.assets.set(pkg.id, { script: pkg.script ?? '', css: pkg.css ?? '', theme: pkg.theme ?? null, settings: pkg.settings ?? [] })
      return plugin
    })
  }
  #installs = 0
  async update(id: string, change: { readonly enabled?: boolean; readonly granted?: readonly string[] }): Promise<PluginSummary> {
    return this.#guard(() => {
      const current = this.plugins.find((entry) => entry.id === id)
      if (!current) throw Object.assign(new Error('not found'), { code: 'plugin_not_found' })
      const next = { ...current, ...(change.enabled === undefined ? {} : { enabled: change.enabled }), ...(change.granted ? { granted: [...change.granted] } : {}) }
      this.plugins = this.plugins.map((entry) => (entry.id === id ? next : entry))
      return next
    })
  }
  async remove(id: string): Promise<void> {
    this.#guard(() => { this.plugins = this.plugins.filter((entry) => entry.id !== id) })
  }
  async storageGet(id: string): Promise<Record<string, string>> { return { ...(this.values.get(id) ?? {}) } }
  readonly writes: Record<string, string | null>[] = []
  async storageUpdate(id: string, changes: Readonly<Record<string, string | null>>): Promise<Record<string, string>> {
    return this.#guard(() => {
      const values = { ...(this.values.get(id) ?? {}) }
      for (const [key, value] of Object.entries(changes)) {
        if (value === null) delete values[key]
        else values[key] = value
      }
      this.writes.push({ ...changes })
      this.values.set(id, values)
      return { ...values }
    })
  }
  readonly secretWrites: { id: string; key: string; set: boolean }[] = []
  async secretSet(id: string, key: string, value: string | null): Promise<string[]> {
    return this.#guard(() => {
      const plugin = this.plugins.find((entry) => entry.id === id)
      if (!plugin) throw Object.assign(new Error('not found'), { code: 'plugin_not_found' })
      this.secretWrites.push({ id, key, set: value !== null })
      const secrets = value === null ? plugin.secrets.filter((entry) => entry !== key) : [...new Set([...plugin.secrets, key])].sort()
      this.plugins = this.plugins.map((entry) => (entry.id === id ? { ...entry, secrets } : entry))
      return secrets
    })
  }
  async devLink(path: string): Promise<PluginSummary> {
    return this.#guard(() => {
      const plugin = summary({ id: 'dev.local', name: 'Local plugin', dev: { path, loadedAt: '2026-01-01T00:00:00Z', problem: '' } })
      this.plugins = [...this.plugins.filter((entry) => entry.id !== plugin.id), plugin]
      return plugin
    })
  }
  downloads: Record<string, unknown> = {}
  async download(url: string): Promise<{ readonly package: unknown; readonly source: string }> {
    return this.#guard(() => {
      if (!(url in this.downloads)) throw Object.assign(new Error('Nothing was found at that link.'), { code: 'download_failed' })
      return { package: this.downloads[url], source: url }
    })
  }
  async devUnlink(id: string): Promise<PluginSummary> {
    return this.#guard(() => {
      const current = this.plugins.find((entry) => entry.id === id)
      if (!current) throw Object.assign(new Error('not found'), { code: 'plugin_not_found' })
      const { dev: _dev, ...next } = current
      this.plugins = this.plugins.map((entry) => (entry.id === id ? next : entry))
      return next
    })
  }
  subscribe(listener: (plugins: PluginSummary[]) => void): () => void {
    this.listeners.add(listener)
    return () => { this.listeners.delete(listener) }
  }
  /** Another window changed the store. */
  emit(): void { for (const listener of this.listeners) listener([...this.plugins]) }

  #guard<T>(run: () => T): T {
    const failure = this.failNext
    this.failNext = null
    if (failure) throw failure
    return run()
  }
}

/** A file picker the test answers by hand: `next` is what the next open picks. */
export class FakeFiles implements FilesPort {
  next: PickedFile | null = null
  saveAnswer = true
  readonly saved: { name: string; text: string }[] = []
  readonly opened: OpenSpec[] = []
  async open(spec: OpenSpec): Promise<PickedFile | null> { this.opened.push(spec); return this.next }
  async save(spec: { readonly name: string; readonly text: string }): Promise<boolean> {
    if (this.saveAnswer) this.saved.push({ name: spec.name, text: spec.text })
    return this.saveAnswer
  }
}

export const FAKE_THEME: ThemeInfo = {
  mode: 'dark',
  tokens: { background: '#111', surface: '#222', text: '#eee', muted: '#999', border: '#333', accent: '#08f', success: '#0a0', warning: '#fa0', danger: '#f33', info: '#09f' },
}

export class FakeTheme implements ThemePort {
  current: ThemeInfo = FAKE_THEME
  readonly listeners = new Set<() => void>()
  read(): ThemeInfo { return this.current }
  subscribe(listener: () => void): () => void {
    this.listeners.add(listener)
    return () => { this.listeners.delete(listener) }
  }
  change(next: ThemeInfo): void {
    this.current = next
    for (const listener of this.listeners) listener()
  }
}

/** The host dependencies beyond the store and the control plane. */
export function hostExtras(bus = new PluginBus()) {
  return { bus, files: new FakeFiles(), sound: vi.fn(), theme: new FakeTheme(), renderSlot: vi.fn(() => null) }
}
