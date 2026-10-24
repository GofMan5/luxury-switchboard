import type { ExtensionPoints } from '../../../shared/contracts/extensions'
import { PLUGIN_LIMIT, packageSizeProblem, type PluginPackage } from '../domain/manifest'
import { effectiveGrants } from '../domain/permissions'
import { IDLE_RUNTIME, splitPageKey, type PluginRuntime, type PluginSummary } from '../domain/plugin'
import type { PageEvent } from '../domain/protocol'
import { PluginHost, revisionOf, type PluginPage } from './plugin-host'
import type { Announcement, BootContext, ControlPort, PluginsPort, WorkerFactory } from './ports'

export interface PluginsState {
  readonly phase: 'loading' | 'ready' | 'error'
  readonly plugins: readonly PluginSummary[]
  readonly runtimes: Readonly<Record<string, PluginRuntime>>
  /** Pages running plugins registered, by `<pluginId>/<pageId>`. */
  readonly pages: Readonly<Record<string, PluginPage>>
  /** The plugin id an operation is running for, or `install`. */
  readonly pending: string
  readonly error: string
  /** Launched in safe mode: plugins are listed and manageable, never run. */
  readonly safeMode: boolean
}

export type InstallResult = { readonly ok: true; readonly plugin: PluginSummary } | { readonly ok: false; readonly message: string }

export interface PluginsDeps {
  readonly port: PluginsPort
  readonly control: ControlPort
  readonly workers: WorkerFactory
  readonly extensions: ExtensionPoints
  readonly notify: (announcement: Announcement) => void
  readonly navigate: (route: string) => void
  readonly isRoute: (route: string) => boolean
  readonly app: BootContext['app']
  readonly safeMode: boolean
}

const ERRORS: Readonly<Record<string, string>> = {
  invalid_plugin: 'The control plane refused this package',
  plugin_not_found: 'That plugin is no longer installed',
  plugin_too_large: 'This plugin is too large to install',
  plugin_limit: `At most ${PLUGIN_LIMIT} plugins can be installed; remove one first`,
  plugin_storage_full: 'The plugin’s storage is full',
  secure_storage_unavailable: 'Secure storage is unavailable, so plugins cannot be saved',
  plugin_save_failed: 'Plugins could not be saved',
  command_too_large: 'This package is too large to send in one command',
}

/**
 * The plugin manager and runtime. It mirrors the control plane's plugin list
 * and keeps exactly one host per enabled plugin in step with it: a new
 * revision, a changed grant or a re-enable restarts the host; disabling or
 * removing stops it and takes back everything it contributed.
 */
export class PluginsModel {
  readonly #deps: PluginsDeps
  #state: PluginsState
  #listeners = new Set<() => void>()
  #hosts = new Map<string, PluginHost>()
  /** Why an enabled plugin could not be loaded from the store. */
  #loadFailures = new Map<string, string>()
  #unsubscribe: (() => void) | null = null
  #queue: Promise<void> = Promise.resolve()
  #latest: readonly PluginSummary[] | null = null
  #disposed = false
  #notifyScheduled = false

  constructor(deps: PluginsDeps) {
    this.#deps = deps
    this.#state = { phase: 'loading', plugins: [], runtimes: {}, pages: {}, pending: '', error: '', safeMode: deps.safeMode }
  }

  snapshot = (): PluginsState => this.#state

  subscribe = (listener: () => void): (() => void) => {
    this.#listeners.add(listener)
    return () => { this.#listeners.delete(listener) }
  }

  async connect(): Promise<void> {
    this.#unsubscribe ??= this.#deps.port.subscribe((plugins) => this.#reconcile(plugins))
    await this.refresh()
  }

  async refresh(): Promise<void> {
    try {
      const plugins = await this.#deps.port.list()
      await this.#reconcile(plugins)
    } catch {
      if (!this.#disposed) this.#set({ ...this.#state, phase: this.#state.phase === 'ready' ? 'ready' : 'error', error: 'Plugins are unavailable' })
    }
  }

  /** Installs or upgrades a package with the permissions the operator ticked.
   * Grants are trimmed to what the package declares before they are sent. */
  async install(pkg: PluginPackage, granted: readonly string[]): Promise<InstallResult> {
    if (this.#state.pending) return { ok: false, message: 'Another plugin change is still running' }
    const grants = effectiveGrants(pkg.permissions, granted)
    const upgrade = this.#state.plugins.some((plugin) => plugin.id === pkg.id)
    if (!upgrade && this.#state.plugins.length >= PLUGIN_LIMIT) return { ok: false, message: ERRORS.plugin_limit }
    const sizeProblem = packageSizeProblem(pkg, grants)
    if (sizeProblem) return { ok: false, message: sizeProblem }
    this.#set({ ...this.#state, pending: 'install', error: '' })
    try {
      const plugin = await this.#deps.port.install(pkg, grants)
      await this.refresh()
      return { ok: true, plugin }
    } catch (error) {
      return { ok: false, message: describe(error) }
    } finally {
      this.#set({ ...this.#state, pending: '' })
    }
  }

  setEnabled(id: string, enabled: boolean): Promise<boolean> {
    return this.#change(id, { enabled })
  }

  /** Replaces what the plugin may do; it restarts with the new grants. */
  setGrants(id: string, granted: readonly string[]): Promise<boolean> {
    const plugin = this.#state.plugins.find((entry) => entry.id === id)
    if (!plugin) return Promise.resolve(false)
    return this.#change(id, { granted: effectiveGrants(plugin.permissions, granted) })
  }

  async remove(id: string): Promise<boolean> {
    if (this.#state.pending) return false
    this.#set({ ...this.#state, pending: id, error: '' })
    try {
      await this.#deps.port.remove(id)
      await this.refresh()
      return true
    } catch (error) {
      this.#set({ ...this.#state, error: describe(error) })
      return false
    } finally {
      this.#set({ ...this.#state, pending: '' })
    }
  }

  /** Starts a crashed or stopped plugin again from its stored code. */
  restart(id: string): Promise<void> {
    return this.#enqueue(async () => {
      this.#hosts.get(id)?.stop()
      this.#hosts.delete(id)
      await this.#sync(this.#state.plugins, true)
    })
  }

  /** Forwards what the operator did on a plugin page to its plugin. */
  sendPageEvent(key: string, event: PageEvent): void {
    const parts = splitPageKey(key)
    if (!parts) return
    this.#hosts.get(parts.pluginId)?.dispatch(parts.pageId, event)
  }

  clearError(): void {
    if (this.#state.error) this.#set({ ...this.#state, error: '' })
  }

  dispose(): void {
    this.#disposed = true
    this.#unsubscribe?.()
    this.#unsubscribe = null
    for (const host of this.#hosts.values()) host.stop()
    this.#hosts.clear()
    this.#listeners.clear()
  }

  async #change(id: string, change: { enabled?: boolean; granted?: readonly string[] }): Promise<boolean> {
    if (this.#state.pending) return false
    this.#set({ ...this.#state, pending: id, error: '' })
    try {
      await this.#deps.port.update(id, change)
      await this.refresh()
      return true
    } catch (error) {
      this.#set({ ...this.#state, error: describe(error) })
      return false
    } finally {
      this.#set({ ...this.#state, pending: '' })
    }
  }

  /** Lists arrive from refreshes and from `plugins.changed`; they are applied
   * one at a time, and only the newest one still waiting is applied. */
  #reconcile(plugins: readonly PluginSummary[]): Promise<void> {
    this.#latest = plugins
    return this.#enqueue(async () => {
      const latest = this.#latest
      if (!latest) return
      this.#latest = null
      await this.#sync(latest, false)
    })
  }

  #enqueue(task: () => Promise<void>): Promise<void> {
    const run = this.#queue.then(() => (this.#disposed ? undefined : task()))
    this.#queue = run.catch(() => undefined)
    return run
  }

  async #sync(plugins: readonly PluginSummary[], restartCrashed: boolean): Promise<void> {
    const ids = new Set(plugins.map((plugin) => plugin.id))
    for (const [id, host] of this.#hosts) {
      if (!ids.has(id)) {
        host.stop()
        this.#hosts.delete(id)
      }
    }
    this.#set({ ...this.#state, phase: 'ready', plugins: [...plugins], error: this.#state.phase === 'error' ? '' : this.#state.error })
    for (const plugin of plugins) {
      if (this.#disposed) return
      const host = this.#hosts.get(plugin.id)
      if (!plugin.enabled) {
        if (host) {
          host.stop('disabled')
          this.#hosts.delete(plugin.id)
        }
        continue
      }
      if (this.#deps.safeMode) continue
      const grants = effectiveGrants(plugin.permissions, plugin.granted)
      const revision = revisionOf(plugin.updatedAt, grants)
      if (host && host.revision === revision && (host.runtime().state !== 'crashed' || !restartCrashed)) continue
      host?.stop()
      this.#hosts.delete(plugin.id)
      await this.#start(plugin, grants)
    }
    this.#publish()
  }

  async #start(plugin: PluginSummary, grants: ReturnType<typeof effectiveGrants>): Promise<void> {
    let detail
    try {
      detail = await this.#deps.port.get(plugin.id)
    } catch (error) {
      this.#loadFailures.set(plugin.id, `The plugin could not be loaded: ${describe(error)}.`)
      return
    }
    this.#loadFailures.delete(plugin.id)
    if (this.#disposed) return
    const host = new PluginHost(detail, grants, {
      workers: this.#deps.workers,
      control: this.#deps.control,
      storage: this.#deps.port,
      extensions: this.#deps.extensions,
      notify: this.#deps.notify,
      navigate: this.#deps.navigate,
      isRoute: this.#deps.isRoute,
      app: this.#deps.app,
      onChange: () => this.#schedulePublish(),
    })
    this.#hosts.set(plugin.id, host)
    host.start()
  }

  /** Hosts change state in bursts (a render, a log line, a status); one
   * microtask folds a burst into one snapshot. */
  #schedulePublish(): void {
    if (this.#notifyScheduled) return
    this.#notifyScheduled = true
    queueMicrotask(() => {
      this.#notifyScheduled = false
      if (!this.#disposed) this.#publish()
    })
  }

  #publish(): void {
    const runtimes: Record<string, PluginRuntime> = {}
    const pages: Record<string, PluginPage> = {}
    for (const plugin of this.#state.plugins) {
      const host = this.#hosts.get(plugin.id)
      if (host) {
        runtimes[plugin.id] = host.runtime()
        for (const page of host.pages()) pages[page.key] = page
      } else if (!plugin.enabled) {
        runtimes[plugin.id] = { ...IDLE_RUNTIME, state: 'disabled' }
      } else if (this.#deps.safeMode) {
        runtimes[plugin.id] = { ...IDLE_RUNTIME, state: 'stopped', reason: 'Safe mode is on: no plugin runs until you leave it.' }
      } else {
        const failure = this.#loadFailures.get(plugin.id)
        runtimes[plugin.id] = failure ? { ...IDLE_RUNTIME, state: 'crashed', reason: failure } : IDLE_RUNTIME
      }
    }
    this.#set({ ...this.#state, runtimes: reuse(this.#state.runtimes, runtimes), pages: reuse(this.#state.pages, pages) })
  }

  #set(state: PluginsState): void {
    this.#state = state
    for (const listener of this.#listeners) listener()
  }
}

/** Keeps the previous record when nothing in it changed, so selectors on one
 * page or one runtime do not re-render for every unrelated change. */
function reuse<T>(previous: Readonly<Record<string, T>>, next: Record<string, T>): Readonly<Record<string, T>> {
  const keys = Object.keys(next)
  if (keys.length !== Object.keys(previous).length) return next
  return keys.every((key) => previous[key] === next[key]) ? previous : next
}

export function describe(error: unknown): string {
  const code = error instanceof Error && 'code' in error && typeof error.code === 'string' ? error.code : ''
  if (code === 'invalid_plugin' && error instanceof Error && error.message.startsWith('Plugin ')) return `The control plane refused this package: ${error.message.slice('Plugin '.length)}`
  if (code && ERRORS[code]) return ERRORS[code]
  return error instanceof Error && error.message ? error.message : 'The plugin change failed'
}

