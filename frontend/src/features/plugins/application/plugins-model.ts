import type { ReactNode } from 'react'
import type { ExtensionPoints, SlotContext } from '../../../shared/contracts/extensions'
import type { CanvasChannel } from '../domain/canvas'
import { PLUGIN_LIMIT, checkPackage, packageSizeProblem, type PluginPackage } from '../domain/manifest'
import { effectiveGrants } from '../domain/permissions'
import { IDLE_RUNTIME, splitPageKey, type PluginRuntime, type PluginSummary } from '../domain/plugin'
import { isRecord, type PageEvent } from '../domain/protocol'
import { acceptValue, isSecretSetting, secretProblem, settingStorageKey, settingValues, valueProblem, type PluginSetting, type SettingValue } from '../domain/settings'
import type { ToneSpec } from '../domain/specs'
import { PluginBus } from './plugin-bus'
import { PluginHost, revisionOf, type PluginDialog, type PluginPage, type PluginSurface } from './plugin-host'
import type { Announcement, BootContext, ConfirmRequest, ControlPort, FilesPort, PluginsPort, ThemePort, WorkerFactory } from './ports'

export interface PluginsState {
  readonly phase: 'loading' | 'ready' | 'error'
  readonly plugins: readonly PluginSummary[]
  readonly runtimes: Readonly<Record<string, PluginRuntime>>
  /** Pages running plugins registered, by `<pluginId>/<pageId>`. */
  readonly pages: Readonly<Record<string, PluginPage>>
  /** Slot surfaces running plugins registered, by `<pluginId>/<surface>`. */
  readonly surfaces: Readonly<Record<string, PluginSurface>>
  /** The dialog each running plugin has open, by plugin id. */
  readonly dialogs: Readonly<Record<string, PluginDialog>>
  /** The plugin id an operation is running for, or `install`. */
  readonly pending: string
  readonly error: string
  /** Launched in safe mode: plugins are listed and manageable, never run. */
  readonly safeMode: boolean
  /** The question a plugin is asking the operator right now, if any; others
   * wait their turn. */
  readonly asking: PluginQuestion | null
}

export interface PluginQuestion extends ConfirmRequest {
  readonly id: number
}

/** A plugin's settings as the editor shows them. */
export interface PluginSettingsView {
  readonly settings: readonly PluginSetting[]
  readonly values: Readonly<Record<string, SettingValue>>
}

export type SaveResult = { readonly ok: true } | { readonly ok: false; readonly message: string }

export type InstallResult = { readonly ok: true; readonly plugin: PluginSummary } | { readonly ok: false; readonly message: string }

export type DownloadResult =
  | { readonly ok: true; readonly pkg: PluginPackage; readonly source: string }
  | { readonly ok: false; readonly message: string; readonly problems?: readonly string[] }

export interface PluginsDeps {
  readonly port: PluginsPort
  readonly control: ControlPort
  readonly workers: WorkerFactory
  readonly extensions: ExtensionPoints
  readonly notify: (announcement: Announcement) => void
  readonly clipboard: (text: string) => Promise<void>
  readonly navigate: (route: string) => void
  readonly isRoute: (route: string) => boolean
  readonly app: BootContext['app']
  readonly safeMode: boolean
  readonly files: FilesPort
  readonly sound: (tone: ToneSpec) => void
  readonly theme: ThemePort
  readonly renderSlot: (pluginId: string, surface: string, context: SlotContext) => ReactNode
}

/** What a canvas of a plugin that is not running talks to. */
const IDLE_CANVAS: CanvasChannel = { subscribe: () => () => undefined, input: () => undefined, visible: () => undefined }

const ERRORS: Readonly<Record<string, string>> = {
  invalid_plugin: 'The control plane refused this package',
  plugin_not_found: 'That plugin is no longer installed',
  plugin_too_large: 'This plugin is too large to install',
  plugin_limit: `At most ${PLUGIN_LIMIT} plugins can be installed; remove one first`,
  plugin_storage_full: 'The plugin’s storage is full',
  secure_storage_unavailable: 'Secure storage is unavailable, so plugins cannot be saved',
  plugin_save_failed: 'Plugins could not be saved',
  command_too_large: 'This package is too large to send in one command',
  plugin_dev_limit: 'No more plugin folders can be linked; unlink one first',
  plugin_dev_linked: 'That plugin is already loaded from a folder',
  plugin_not_linked: 'That plugin is not loaded from a folder',
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
  #questions: { readonly question: PluginQuestion; readonly settle: (answer: boolean) => void }[] = []
  #nextQuestion = 1
  readonly #bus = new PluginBus()

  constructor(deps: PluginsDeps) {
    this.#deps = deps
    this.#state = { phase: 'loading', plugins: [], runtimes: {}, pages: {}, surfaces: {}, dialogs: {}, pending: '', error: '', safeMode: deps.safeMode, asking: null }
  }

  snapshot = (): PluginsState => this.#state

  subscribe = (listener: () => void): (() => void) => {
    this.#listeners.add(listener)
    return () => { this.#listeners.delete(listener) }
  }

  async connect(): Promise<void> {
    this.#unsubscribe ??= this.#deps.port.subscribe((plugins) => this.#reconcile(plugins))
    // Hooks a previous window registered have no host answering them any more,
    // safe mode included: the sidecar outlives the reload.
    await this.#deps.control.call('relayhooks.reset', {}).catch(() => undefined)
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
   * Grants are trimmed to what the package declares before they are sent.
   * `source` is the link a downloaded package came from; any other install
   * makes the control plane forget the previous link. */
  async install(pkg: PluginPackage, granted: readonly string[], source?: string): Promise<InstallResult> {
    if (this.#state.pending) return { ok: false, message: 'Another plugin change is still running' }
    const grants = effectiveGrants(pkg.permissions, granted)
    const upgrade = this.#state.plugins.some((plugin) => plugin.id === pkg.id)
    if (!upgrade && this.#state.plugins.length >= PLUGIN_LIMIT) return { ok: false, message: ERRORS.plugin_limit }
    const sizeProblem = packageSizeProblem(pkg, grants)
    if (sizeProblem) return { ok: false, message: sizeProblem }
    this.#set({ ...this.#state, pending: 'install', error: '' })
    try {
      const plugin = await this.#deps.port.install(pkg, grants, source || undefined)
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
      await this.#sync(this.#state.plugins)
    })
  }

  /** Forwards what the operator did on a plugin page to its plugin. */
  sendPageEvent(key: string, event: PageEvent): void {
    const parts = splitPageKey(key)
    if (!parts) return
    this.#hosts.get(parts.pluginId)?.dispatch(parts.pageId, event)
  }

  /** Forwards what the operator did on a slot surface (`widget:<id>`, …). */
  sendSurfaceEvent(pluginId: string, surface: string, event: PageEvent): void {
    this.#hosts.get(pluginId)?.dispatch(surface, event)
  }

  /** The operator pressed a button of a plugin's dialog (`null`: dismissed). */
  answerDialog(pluginId: string, id: string, action: string | null, form: PageEvent['form']): void {
    this.#hosts.get(pluginId)?.answerDialog(id, action, form)
  }

  /** How a plugin's canvases reach its running script. */
  canvas(pluginId: string): CanvasChannel {
    return this.#hosts.get(pluginId)?.canvas ?? IDLE_CANVAS
  }

  /** The operator answered the question on screen. */
  answer(id: number, yes: boolean): void {
    const index = this.#questions.findIndex((entry) => entry.question.id === id)
    if (index === -1) return
    const [entry] = this.#questions.splice(index, 1)
    entry.settle(yes)
    this.#showQuestion()
  }

  /** A plugin's declared settings and their current values. */
  async loadSettings(id: string): Promise<PluginSettingsView> {
    const [detail, storage] = await Promise.all([this.#deps.port.get(id), this.#deps.port.storageGet(id)])
    return { settings: detail.settings, values: settingValues(detail.settings, storage) }
  }

  /**
   * Saves the operator's setting values in one write; a value equal to the
   * default is stored as no value, so a later default change reaches it. A
   * running plugin hears about the change right away.
   */
  async saveSettings(id: string, settings: readonly PluginSetting[], values: Readonly<Record<string, unknown>>): Promise<SaveResult> {
    if (this.#state.pending) return { ok: false, message: 'Another plugin change is still running' }
    const changes: Record<string, string | null> = {}
    for (const setting of settings) {
      if (isSecretSetting(setting) || !Object.hasOwn(values, setting.key)) continue
      const value = acceptValue(setting, values[setting.key])
      if (value === null) return { ok: false, message: `${setting.label}: ${valueProblem(setting, values[setting.key])}` }
      changes[settingStorageKey(setting.key)] = value === setting.default ? null : JSON.stringify(value)
    }
    if (Object.keys(changes).length === 0) return { ok: true }
    this.#set({ ...this.#state, pending: id, error: '' })
    try {
      const stored = await this.#deps.port.storageUpdate(id, changes)
      this.#hosts.get(id)?.storageChanged(stored)
      return { ok: true }
    } catch (error) {
      return { ok: false, message: describe(error) }
    } finally {
      this.#set({ ...this.#state, pending: '' })
    }
  }

  /**
   * Sets (`value`) or clears (`null`) one secret. The value goes straight to
   * the control plane and is never kept here; the answer is the list of keys
   * that now have one.
   */
  async secretSet(id: string, key: string, value: string | null): Promise<SaveResult> {
    if (value !== null) {
      const problem = secretProblem(value)
      if (problem) return { ok: false, message: problem }
    }
    try {
      const secrets = await this.#deps.port.secretSet(id, key, value)
      this.#set({ ...this.#state, plugins: this.#state.plugins.map((plugin) => (plugin.id === id ? { ...plugin, secrets } : plugin)) })
      this.#hosts.get(id)?.secretsChanged(secrets)
      return { ok: true }
    } catch (error) {
      return { ok: false, message: describe(error) }
    }
  }

  /** Loads (or reloads) a plugin from a folder on this computer. */
  async devLink(path: string): Promise<InstallResult> {
    if (this.#state.pending) return { ok: false, message: 'Another plugin change is still running' }
    const trimmed = path.trim()
    if (!trimmed) return { ok: false, message: 'Enter the folder that holds plugin.json' }
    this.#set({ ...this.#state, pending: 'install', error: '' })
    try {
      const plugin = await this.#deps.port.devLink(trimmed)
      await this.refresh()
      return { ok: true, plugin }
    } catch (error) {
      return { ok: false, message: describe(error) }
    } finally {
      this.#set({ ...this.#state, pending: '' })
    }
  }

  /** Fetches a package from a link for review; nothing is installed. */
  async download(url: string): Promise<DownloadResult> {
    if (this.#state.pending) return { ok: false, message: 'Another plugin change is still running' }
    const trimmed = url.trim()
    if (!trimmed) return { ok: false, message: 'Paste a link to a plugin' }
    this.#set({ ...this.#state, pending: 'install', error: '' })
    try {
      const answer = await this.#deps.port.download(trimmed)
      const checked = checkPackage(answer.package)
      if (!checked.ok) return { ok: false, message: 'The link does not hold a valid plugin package', problems: checked.problems }
      return { ok: true, pkg: checked.package, source: answer.source }
    } catch (error) {
      return { ok: false, message: describe(error) }
    } finally {
      this.#set({ ...this.#state, pending: '' })
    }
  }

  /** Saves an installed plugin as a package file in the `plugin pack` format,
   * so the file installs again as the same package. Settings values and
   * storage are never part of it. False when it failed or was cancelled. */
  async savePackage(id: string): Promise<boolean> {
    if (this.#state.pending) return false
    this.#set({ ...this.#state, pending: id, error: '' })
    try {
      const raw = await this.#deps.port.export(id)
      const checked = checkPackage(raw)
      if (!checked.ok || !isRecord(raw)) throw new Error('The stored package is not valid, so it cannot be saved')
      return await this.#deps.files.save({ name: `${checked.package.id}-${checked.package.version}.json`, text: packedText(checked.package, raw), mime: 'application/json' })
    } catch (error) {
      this.#set({ ...this.#state, error: describe(error) })
      return false
    } finally {
      this.#set({ ...this.#state, pending: '' })
    }
  }

  /** Stops loading a plugin from its folder; the installed copy stays. */
  async devUnlink(id: string): Promise<boolean> {
    if (this.#state.pending) return false
    this.#set({ ...this.#state, pending: id, error: '' })
    try {
      await this.#deps.port.devUnlink(id)
      await this.refresh()
      return true
    } catch (error) {
      this.#set({ ...this.#state, error: describe(error) })
      return false
    } finally {
      this.#set({ ...this.#state, pending: '' })
    }
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
    for (const entry of this.#questions.splice(0)) entry.settle(false)
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
      await this.#sync(latest)
    })
  }

  #enqueue(task: () => Promise<void>): Promise<void> {
    const run = this.#queue.then(() => (this.#disposed ? undefined : task()))
    this.#queue = run.catch(() => undefined)
    return run
  }

  async #sync(plugins: readonly PluginSummary[]): Promise<void> {
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
      // A plugin this version cannot use never runs, whatever its switch says.
      if (!plugin.enabled || plugin.problem) {
        if (host) {
          host.stop('disabled')
          this.#hosts.delete(plugin.id)
        }
        continue
      }
      if (this.#deps.safeMode) continue
      const grants = effectiveGrants(plugin.permissions, plugin.granted)
      const revision = revisionOf(plugin, grants)
      if (host && host.revision === revision) continue
      host?.stop()
      this.#hosts.delete(plugin.id)
      await this.#start(plugin, grants)
    }
    this.#publish()
  }

  async #start(plugin: PluginSummary, grants: ReturnType<typeof effectiveGrants>): Promise<void> {
    let detail
    let storage: Record<string, string> | null = null
    try {
      detail = await this.#deps.port.get(plugin.id)
      // Settings are part of the boot facts, so a plugin that has them
      // starts with the operator's values, never the defaults first.
      if (detail.settings.length > 0) storage = await this.#deps.port.storageGet(plugin.id)
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
      confirm: (request, signal) => this.#ask(request, signal),
      clipboard: this.#deps.clipboard,
      navigate: this.#deps.navigate,
      isRoute: this.#deps.isRoute,
      app: this.#deps.app,
      onChange: () => this.#schedulePublish(),
      bus: this.#bus,
      files: this.#deps.files,
      sound: this.#deps.sound,
      theme: this.#deps.theme,
      renderSlot: this.#deps.renderSlot,
    }, storage)
    this.#hosts.set(plugin.id, host)
    host.start()
  }

  /** Queues a plugin's question; it resolves false when the plugin stops
   * before the operator answers. Hosts allow one open question each, so the
   * queue is bounded by the number of plugins. */
  #ask(request: ConfirmRequest, signal: AbortSignal): Promise<boolean> {
    if (signal.aborted || this.#disposed) return Promise.resolve(false)
    return new Promise<boolean>((resolve) => {
      const question: PluginQuestion = { ...request, id: this.#nextQuestion++ }
      const abort = () => this.answer(question.id, false)
      signal.addEventListener('abort', abort, { once: true })
      this.#questions.push({ question, settle: (answer) => { signal.removeEventListener('abort', abort); resolve(answer) } })
      this.#showQuestion()
    })
  }

  #showQuestion(): void {
    const asking = this.#questions[0]?.question ?? null
    if (asking !== this.#state.asking) this.#set({ ...this.#state, asking })
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
    const surfaces: Record<string, PluginSurface> = {}
    const dialogs: Record<string, PluginDialog> = {}
    for (const plugin of this.#state.plugins) {
      const host = this.#hosts.get(plugin.id)
      if (host) {
        runtimes[plugin.id] = host.runtime()
        for (const page of host.pages()) pages[page.key] = page
        for (const surface of host.surfaces()) surfaces[surface.key] = surface
        const dialog = host.dialog()
        if (dialog) dialogs[plugin.id] = dialog
      } else if (!plugin.enabled) {
        runtimes[plugin.id] = { ...IDLE_RUNTIME, state: 'disabled' }
      } else if (this.#deps.safeMode) {
        runtimes[plugin.id] = { ...IDLE_RUNTIME, state: 'stopped', reason: 'Safe mode is on: no plugin runs until you leave it.' }
      } else {
        const failure = this.#loadFailures.get(plugin.id)
        runtimes[plugin.id] = failure ? { ...IDLE_RUNTIME, state: 'crashed', reason: failure } : IDLE_RUNTIME
      }
    }
    this.#set({ ...this.#state, runtimes: reuse(this.#state.runtimes, runtimes), pages: reuse(this.#state.pages, pages), surfaces: reuse(this.#state.surfaces, surfaces), dialogs: reuse(this.#state.dialogs, dialogs) })
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

/** The checked fields in package order with their values as written, two-space
 * indented with a final newline: what `scripts/plugin/package.mjs` packs. */
function packedText(pkg: PluginPackage, raw: Record<string, unknown>): string {
  return `${JSON.stringify(Object.fromEntries(Object.keys(pkg).map((key) => [key, raw[key]])), null, 2)}\n`
}

export function describe(error: unknown): string {
  const code = error instanceof Error && 'code' in error && typeof error.code === 'string' ? error.code : ''
  if (code === 'invalid_plugin' && error instanceof Error && error.message.startsWith('Plugin ')) return `The control plane refused this package: ${error.message.slice('Plugin '.length)}`
  // The folder's own problem (a bad plugin.json, a missing file) is the message.
  if (code === 'plugin_dev_problem' && error instanceof Error && error.message) return error.message
  if (code && ERRORS[code]) return ERRORS[code]
  return error instanceof Error && error.message ? error.message : 'The plugin change failed'
}

