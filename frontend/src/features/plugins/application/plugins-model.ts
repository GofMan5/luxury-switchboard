import type { ReactNode } from 'react'
import type { ExtensionPoints, SlotContext } from '../../../shared/contracts/extensions'
import type { CanvasChannel } from '../domain/canvas'
import { PLUGIN_LIMIT, checkPackage, compareVersions, needsNewerApp, packageSizeProblem, type PluginPackage } from '../domain/manifest'
import { effectiveGrants, initialGrants, upgradeAsksMore } from '../domain/permissions'
import { IDLE_RUNTIME, STORAGE_LIMITS, formatLog, splitPageKey, type CollectionUsage, type LogEntry, type PluginRuntime, type PluginSummary } from '../domain/plugin'
import { isRecord, type PageEvent } from '../domain/protocol'
import { acceptValue, isSecretSetting, isSettingStorageKey, secretProblem, settingStorageKey, settingValues, valueProblem, type PluginSetting, type SettingValue } from '../domain/settings'
import type { ToneSpec } from '../domain/specs'
import type { JournalPage, JournalQuery } from '../domain/journal'
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
  /** True while installed plugins' links are being checked for updates. */
  readonly checking: boolean
  /** The latest update check of each plugin installed from a link, by id. */
  readonly updates: Readonly<Record<string, UpdateCheck>>
}

/** What a plugin's link serves compared with the installed version. A link
 * that now holds another plugin, or a version that cannot be ordered, is an
 * error rather than an offer. `changed` is the same version with different
 * contents — new code under an old number — and always waits for a review;
 * `updatedAt` is the installed revision it was compared with. */
export type UpdateCheck =
  | { readonly state: 'newer'; readonly pkg: PluginPackage; readonly source: string }
  | { readonly state: 'changed'; readonly pkg: PluginPackage; readonly source: string; readonly updatedAt: string }
  | { readonly state: 'current' }
  | { readonly state: 'error'; readonly message: string }

export interface PluginQuestion extends ConfirmRequest {
  readonly id: number
}

/** A plugin's settings as the editor shows them. */
export interface PluginSettingsView {
  readonly settings: readonly PluginSetting[]
  readonly values: Readonly<Record<string, SettingValue>>
}

/** What a plugin stores on this machine. `values` leaves out the operator's
 * settings, which live in the same storage and count against its limit. */
export interface PluginDataUsage {
  readonly storage: { readonly values: number; readonly bytes: number; readonly limitBytes: number; readonly limitKeys: number }
  readonly collections: CollectionUsage
}

export type DataResult = { readonly ok: true; readonly usage: PluginDataUsage } | { readonly ok: false; readonly message: string }

export type SaveResult = { readonly ok: true } | { readonly ok: false; readonly message: string }

/** What `updateAll` did: who was updated, who was left for the usual review
 * (the update asks for more, needs a newer app, or the plugin is switched
 * off), and who failed (by id). */
/** Splits the offered updates: `quiet` ones ask for nothing the operator has
 * not already decided, so Update all installs them with the previous grants;
 * the rest (new permissions or hosts, a newer app, a switched-off plugin, which
 * installing would switch on) wait for a review on their own row. */
export function sortUpdates(plugins: readonly PluginSummary[], updates: Readonly<Record<string, UpdateCheck>>, appVersion: string): {
  quiet: { plugin: PluginSummary; check: Extract<UpdateCheck, { state: 'newer' }> }[]
  review: string[]
} {
  const quiet: { plugin: PluginSummary; check: Extract<UpdateCheck, { state: 'newer' }> }[] = []
  const review: string[] = []
  for (const plugin of plugins) {
    const check = updates[plugin.id]
    if (check?.state === 'changed') { review.push(plugin.id); continue }
    if (check?.state !== 'newer') continue
    const asks = !plugin.enabled || needsNewerApp(check.pkg.minAppVersion, appVersion)
      || upgradeAsksMore(plugin, { permissions: check.pkg.permissions, hosts: check.pkg.network?.hosts ?? [] })
    if (asks) review.push(plugin.id)
    else quiet.push({ plugin, check })
  }
  return { quiet, review }
}

export interface UpdateAllResult {
  readonly updated: readonly PluginSummary[]
  readonly review: readonly string[]
  readonly failed: readonly { readonly id: string; readonly message: string }[]
}

export type InstallResult = { readonly ok: true; readonly plugin: PluginSummary } | { readonly ok: false; readonly message: string }

export type DownloadResult =
  | { readonly ok: true; readonly pkg: PluginPackage; readonly source: string }
  | { readonly ok: false; readonly message: string; readonly problems?: readonly string[] }

export type JournalResult = { readonly ok: true; readonly page: JournalPage } | { readonly ok: false; readonly message: string }

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

/** Crash recovery: three automatic restarts inside five minutes, then the
 * operator decides. The ladder gives a transient fault time to clear. */
const RESTART_BACKOFF_MS = [2_000, 10_000, 60_000]
const CRASH_WINDOW_MS = 5 * 60_000

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
  /** When each plugin last crashed, inside the retry window. */
  #crashes = new Map<string, number[]>()
  #seenCrash = new WeakSet<PluginHost>()
  #retries = new Map<string, ReturnType<typeof setTimeout>>()

  constructor(deps: PluginsDeps) {
    this.#deps = deps
    this.#state = { phase: 'loading', plugins: [], runtimes: {}, pages: {}, surfaces: {}, dialogs: {}, pending: '', error: '', safeMode: deps.safeMode, asking: null, checking: false, updates: {} }
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
    this.#set({ ...this.#state, pending: 'install', error: '' })
    try {
      return await this.#install(pkg, granted, source)
    } finally {
      this.#set({ ...this.#state, pending: '' })
    }
  }

  async #install(pkg: PluginPackage, granted: readonly string[], source?: string): Promise<InstallResult> {
    const grants = effectiveGrants(pkg.permissions, granted)
    const upgrade = this.#state.plugins.some((plugin) => plugin.id === pkg.id)
    if (!upgrade && this.#state.plugins.length >= PLUGIN_LIMIT) return { ok: false, message: ERRORS.plugin_limit }
    const sizeProblem = packageSizeProblem(pkg, grants)
    if (sizeProblem) return { ok: false, message: sizeProblem }
    try {
      const plugin = await this.#deps.port.install(pkg, grants, source || undefined)
      // An earlier check described the package this one replaced.
      const { [pkg.id]: _replaced, ...updates } = this.#state.updates
      this.#set({ ...this.#state, updates })
      await this.refresh()
      return { ok: true, plugin }
    } catch (error) {
      return { ok: false, message: describe(error) }
    }
  }

  /** Installs every update the latest check offered, one at a time, keeping
   * each plugin's previous decisions. An update that asks for a permission or
   * host the operator has not decided on, needs a newer app, or belongs to a
   * switched-off plugin is left for the usual review: nothing is ever granted
   * unasked. */
  async updateAll(appVersion: string): Promise<UpdateAllResult> {
    const updated: PluginSummary[] = []
    const failed: { readonly id: string; readonly message: string }[] = []
    if (this.#state.pending) return { updated, review: [], failed }
    const { quiet, review } = sortUpdates(this.#state.plugins, this.#state.updates, appVersion)
    if (!quiet.length && !review.length) return { updated, review, failed }
    this.#set({ ...this.#state, pending: 'update-all', error: '' })
    try {
      for (const { plugin, check } of quiet) {
        if (this.#disposed) break
        const result = await this.#install(check.pkg, initialGrants(check.pkg.permissions, plugin, check.pkg.network?.hosts ?? []), check.source)
        if (result.ok) updated.push(result.plugin)
        else failed.push({ id: plugin.id, message: result.message })
      }
    } finally {
      if (!this.#disposed) this.#set({ ...this.#state, pending: '' })
    }
    return { updated, review, failed }
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

  /** How much the plugin keeps in its storage and collections. */
  async loadData(id: string): Promise<DataResult> {
    try {
      const [storage, collections] = await Promise.all([this.#deps.port.storageGet(id), this.#deps.port.collectionStats(id)])
      return { ok: true, usage: dataUsage(storage, collections) }
    } catch (error) {
      return { ok: false, message: describe(error) }
    }
  }

  /** One page of the activity journal; a failure is unavailable history, never an empty one. */
  async loadJournal(query: JournalQuery): Promise<JournalResult> {
    try {
      return { ok: true, page: await this.#deps.port.listJournal(query) }
    } catch (error) {
      return { ok: false, message: describe(error) }
    }
  }

  /** Deletes the whole activity journal, readable or not. */
  async clearJournal(): Promise<{ readonly ok: true } | { readonly ok: false; readonly message: string }> {
    try {
      await this.#deps.port.clearJournal()
      return { ok: true }
    } catch (error) {
      return { ok: false, message: describe(error) }
    }
  }

  /**
   * Deletes what the plugin stored — its own storage values and every
   * collection — but keeps the operator's settings and secrets. A running
   * plugin restarts so it does not keep working from what it had read.
   */
  async clearData(id: string): Promise<DataResult> {
    if (this.#state.pending) return { ok: false, message: 'Another plugin change is still running' }
    this.#set({ ...this.#state, pending: id, error: '' })
    let result: DataResult = { ok: false, message: 'The plugin page closed before its data was cleared' }
    try {
      // One queued step: the script is stopped and its in-flight writes land
      // before the clear, so nothing it was doing can refill the store.
      await this.#enqueue(async () => {
        const host = this.#hosts.get(id)
        host?.stop()
        this.#hosts.delete(id)
        try {
          await host?.settled()
          const current = await this.#deps.port.storageGet(id)
          const changes = Object.fromEntries(Object.keys(current).filter((key) => !isSettingStorageKey(key)).map((key) => [key, null]))
          const storage = Object.keys(changes).length ? await this.#deps.port.storageUpdate(id, changes) : current
          await this.#deps.port.collectionClearAll(id)
          const collections = await this.#deps.port.collectionStats(id)
          result = { ok: true, usage: dataUsage(storage, collections) }
        } finally {
          if (host) await this.#sync(this.#state.plugins)
        }
      })
      return result
    } catch (error) {
      return { ok: false, message: describe(error) }
    } finally {
      this.#set({ ...this.#state, pending: '' })
    }
  }

  restart(id: string): Promise<void> {
    clearTimeout(this.#retries.get(id))
    this.#retries.delete(id)
    this.#crashes.delete(id)
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

  /** Downloads the link of every plugin installed from one (dev-linked ones
   * reload from their folder) and records what each serves. One at a time:
   * a slow host delays the rest rather than many downloads competing.
   * Nothing is installed; an offer opens the usual review. */
  async checkUpdates(): Promise<{ readonly newer: number; readonly changed: number; readonly failed: number; readonly checked: number }> {
    const targets = this.#state.plugins.filter((plugin) => plugin.source && !plugin.dev)
    if (this.#state.checking || !targets.length) return { newer: 0, changed: 0, failed: 0, checked: 0 }
    this.#set({ ...this.#state, checking: true })
    const results: Record<string, UpdateCheck> = {}
    try {
      for (const plugin of targets) {
        if (this.#disposed) break
        results[plugin.id] = await this.#checkOne(plugin)
      }
    } finally {
      if (!this.#disposed) {
        this.#set({ ...this.#state, checking: false, updates: liveUpdates({ ...this.#state.updates, ...results }, this.#state.plugins) })
      }
    }
    const values = Object.values(results)
    const count = (state: UpdateCheck['state']) => values.filter((check) => check.state === state).length
    return { newer: count('newer'), changed: count('changed'), failed: count('error'), checked: values.length }
  }

  /** The launch check: looks for updates once and announces what it found.
   * It never installs — every offer still goes through the usual review — and
   * stays quiet when nothing is newer or the check could not run. */
  async announceUpdates(): Promise<void> {
    const { newer, changed } = await this.checkUpdates()
    const total = newer + changed
    if (!total || this.#disposed) return
    this.#deps.notify({
      kind: 'plugin', severity: 'info',
      title: total === 1 ? 'A plugin update is available' : `${total} plugin updates are available`,
      body: changed
        ? 'Open Plugins to review the changes before updating. Some links serve changed code under the installed version number.'
        : 'Open Plugins to review the changes before updating.',
    })
  }

  async #checkOne(plugin: PluginSummary): Promise<UpdateCheck> {
    try {
      const answer = await this.#deps.port.download(plugin.source)
      const checked = checkPackage(answer.package)
      if (!checked.ok) return { state: 'error', message: 'The link no longer holds a valid plugin package' }
      if (checked.package.id !== plugin.id) return { state: 'error', message: `The link now serves a different plugin (${checked.package.id})` }
      const order = compareVersions(checked.package.version, plugin.version)
      if (order === null && checked.package.version !== plugin.version) return { state: 'error', message: `Version ${checked.package.version} cannot be compared with ${plugin.version}` }
      if (order !== null && order > 0) return { state: 'newer', pkg: checked.package, source: answer.source }
      if (order !== null && order < 0) return { state: 'current' }
      // The same version number: only the contents can tell whether the
      // author changed the code without saying so. The version string itself
      // is left out, so build metadata alone (`1.0.0+build.2`) is no change.
      const installed = checkPackage(await this.#deps.port.export(plugin.id))
      if (!installed.ok) return { state: 'error', message: 'The installed package could not be read to compare it with the link' }
      return canonicalJson({ ...installed.package, version: '' }) === canonicalJson({ ...checked.package, version: '' })
        ? { state: 'current' }
        : { state: 'changed', pkg: checked.package, source: answer.source, updatedAt: plugin.updatedAt }
    } catch (error) {
      return { state: 'error', message: describe(error) }
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

  /** Copies log lines as plain text. False when the clipboard refused. */
  async copyLog(entries: readonly LogEntry[]): Promise<boolean> {
    try {
      await this.#deps.clipboard(formatLog(entries))
      return true
    } catch (error) {
      this.#set({ ...this.#state, error: `Could not copy the log: ${describe(error)}` })
      return false
    }
  }

  /** Saves log lines as a text file. False when it failed or was cancelled. */
  async saveLog(id: string, entries: readonly LogEntry[]): Promise<boolean> {
    try {
      return await this.#deps.files.save({ name: `${id}-log.txt`, text: formatLog(entries), mime: 'text/plain' })
    } catch (error) {
      this.#set({ ...this.#state, error: `Could not save the log: ${describe(error)}` })
      return false
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
    for (const timer of this.#retries.values()) clearTimeout(timer)
    this.#retries.clear()
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
    this.#set({ ...this.#state, phase: 'ready', plugins: [...plugins], updates: liveUpdates(this.#state.updates, plugins), error: this.#state.phase === 'error' ? '' : this.#state.error })
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
        runtimes[plugin.id] = this.#recover(plugin.id, host)
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

  /** A crashed host is restarted on a backoff ladder; a plugin that keeps
   * crashing inside the window stays down until the operator restarts it.
   * Load failures never reach here: retrying a broken package cannot help. */
  #recover(id: string, host: PluginHost): PluginRuntime {
    const runtime = host.runtime()
    if (runtime.state !== 'crashed') return runtime
    if (!this.#seenCrash.has(host)) {
      this.#seenCrash.add(host)
      const now = Date.now()
      const recent = (this.#crashes.get(id) ?? []).filter((at) => now - at < CRASH_WINDOW_MS)
      recent.push(now)
      this.#crashes.set(id, recent)
      const delay = RESTART_BACKOFF_MS[recent.length - 1]
      if (delay !== undefined) {
        this.#retries.set(id, setTimeout(() => {
          this.#retries.delete(id)
          void this.#enqueue(async () => {
            // Disabled, removed or restarted by hand meanwhile: nothing to do.
            if (this.#hosts.get(id) !== host) return
            host.stop()
            this.#hosts.delete(id)
            await this.#sync(this.#state.plugins)
          })
        }, delay))
      }
    }
    const attempts = this.#crashes.get(id)?.length ?? 0
    const note = this.#retries.has(id)
      ? 'Restarting automatically.'
      : `It crashed ${attempts} times in a few minutes, so it stays stopped until you restart it.`
    return { ...runtime, reason: runtime.reason ? `${runtime.reason} ${note}` : note }
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
/** JSON with every object's keys sorted, so two packages compare by content
 * rather than by the order their fields were written in. */
export function canonicalJson(value: unknown): string {
  return JSON.stringify(value, (_key, item: unknown) => isRecord(item)
    ? Object.fromEntries(Object.keys(item).sort().map((key) => [key, item[key]]))
    : item)
}

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

/** Keeps the checks that still describe an installed plugin: a removed,
 * dev-linked or no-longer-linked plugin, or one already at the offered version,
 * keeps no stale offer. */
function liveUpdates(updates: Readonly<Record<string, UpdateCheck>>, plugins: readonly PluginSummary[]): Record<string, UpdateCheck> {
  const live: Record<string, UpdateCheck> = {}
  for (const plugin of plugins) {
    const check = updates[plugin.id]
    if (!check || !plugin.source || plugin.dev) continue
    if (check.state === 'newer' && compareVersions(check.pkg.version, plugin.version) !== 1) continue
    // Any reinstall since the comparison makes it stale, whatever it installed.
    if (check.state === 'changed' && check.updatedAt !== plugin.updatedAt) continue
    live[plugin.id] = check
  }
  return live
}

function dataUsage(storage: Readonly<Record<string, string>>, collections: CollectionUsage): PluginDataUsage {
  const encoder = new TextEncoder()
  let bytes = 0
  for (const [key, value] of Object.entries(storage)) bytes += encoder.encode(key).length + encoder.encode(value).length
  const values = Object.keys(storage).filter((key) => !isSettingStorageKey(key)).length
  return { storage: { values, bytes, limitBytes: STORAGE_LIMITS.bytes, limitKeys: STORAGE_LIMITS.keys }, collections }
}
