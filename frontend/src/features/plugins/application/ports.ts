import type { ThemeToken } from '../domain/canvas'
import type { PluginPackage } from '../domain/manifest'
import type { JournalPage, JournalQuery } from '../domain/journal'
import type { CollectionUsage, PluginDetail, PluginSummary } from '../domain/plugin'
import type { HostMessage } from '../domain/protocol'
import type { SettingValue } from '../domain/settings'
import type { OpenSpec, SaveSpec } from '../domain/specs'

/** The control plane's plugin store. */
export interface PluginsPort {
  list(signal?: AbortSignal): Promise<PluginSummary[]>
  get(id: string, signal?: AbortSignal): Promise<PluginDetail>
  /** `source` is the https link the package was downloaded from; without it
   * the control plane forgets any earlier link. */
  install(pkg: PluginPackage, granted: readonly string[], source?: string): Promise<PluginSummary>
  update(id: string, change: { readonly enabled?: boolean; readonly granted?: readonly string[] }): Promise<PluginSummary>
  remove(id: string): Promise<void>
  storageGet(id: string): Promise<Record<string, string>>
  /** Applies every change (`null` deletes) in one encrypted write, or none of
   * them. Answers with every value the plugin now has. */
  storageUpdate(id: string, values: Readonly<Record<string, string | null>>): Promise<Record<string, string>>
  collectionStats(id: string): Promise<CollectionUsage>
  /** Deletes every document in every collection; answers how many went. */
  collectionClearAll(id: string): Promise<number>
  /** Sets (`value`) or clears (`null`) one secret setting. Answers with the
   * keys that now have a value; a value is never read back. */
  secretSet(id: string, key: string, value: string | null): Promise<string[]>
  /** Loads a plugin from a folder on this computer for development. */
  devLink(path: string): Promise<PluginSummary>
  /** Stops loading a plugin from its folder. */
  devUnlink(id: string): Promise<PluginSummary>
  /** Fetches a package from a link without installing it; `package` is
   * unchecked JSON and `source` is where it was read from. */
  download(url: string): Promise<{ readonly package: unknown; readonly source: string }>
  /** The installed package as stored, without settings values or storage;
   * unchecked JSON. */
  export(id: string): Promise<unknown>
  /** What happened to plugins, newest first; one plugin when `pluginId` is set. */
  listJournal(query: JournalQuery, signal?: AbortSignal): Promise<JournalPage>
  /** Deletes the whole journal; the only way out of an unreadable one. */
  clearJournal(): Promise<void>
  /** Installs, upgrades, updates and removals — from this window or another. */
  subscribe(listener: (plugins: PluginSummary[]) => void): () => void
}

/** The control plane as a plugin reaches it, after the host's checks. */
export interface ControlPort {
  call(method: string, payload: unknown, signal?: AbortSignal): Promise<unknown>
  subscribe(topic: string, listener: (payload: unknown) => void): () => void
}

/** What a running plugin knows about itself and the app, baked into its worker. */
export interface BootContext {
  readonly plugin: { readonly id: string; readonly name: string; readonly version: string; readonly granted: readonly string[] }
  readonly app: { readonly version: string; readonly capabilities: readonly string[] }
  /** The operator's current setting values, defaults filled in. */
  readonly settings: Readonly<Record<string, SettingValue>>
  /** The keys of the plugin's secret settings that have a value. */
  readonly secrets: readonly string[]
}

/** A question a plugin asks the operator before it does something. */
export interface ConfirmRequest {
  readonly pluginName: string
  readonly title: string
  readonly body: string
  readonly confirmText: string
  readonly tone: 'neutral' | 'danger'
}

/** One isolated execution context for one plugin's script. */
export interface PluginWorker {
  post(message: HostMessage): void
  /** Each message the script posts, unchecked. */
  onMessage(listener: (data: unknown) => void): void
  /** The worker failed to load, threw at top level, or sent something that
   * cannot be received. */
  onError(listener: (message: string) => void): void
  terminate(): void
}

export interface WorkerFactory {
  create(script: string, context: BootContext): PluginWorker
}

export interface Announcement {
  readonly kind: 'plugin'
  readonly severity: 'info' | 'success' | 'warning' | 'danger'
  readonly title: string
  readonly body: string
}

/** The window's current colours, as `ui.theme()` reports them. */
export interface ThemeInfo {
  readonly mode: 'dark' | 'light'
  readonly tokens: Readonly<Record<ThemeToken, string>>
}

export interface ThemePort {
  read(): ThemeInfo
  /** Runs `listener` after the operator changes the theme or mode. */
  subscribe(listener: () => void): () => void
}

/** A file the operator picked; nothing is read until `read()`. */
export interface PickedFile {
  readonly name: string
  readonly size: number
  read(): Promise<string>
}

export interface FilesPort {
  /** Resolves null when the operator cancels (or the signal aborts). */
  open(spec: OpenSpec, signal: AbortSignal): Promise<PickedFile | null>
  /** Resolves false when the operator cancels. */
  save(spec: SaveSpec): Promise<boolean>
}
