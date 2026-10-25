import type { PluginPackage } from '../domain/manifest'
import type { PluginDetail, PluginSummary } from '../domain/plugin'
import type { HostMessage } from '../domain/protocol'
import type { SettingValue } from '../domain/settings'

/** The control plane's plugin store. */
export interface PluginsPort {
  list(signal?: AbortSignal): Promise<PluginSummary[]>
  get(id: string, signal?: AbortSignal): Promise<PluginDetail>
  install(pkg: PluginPackage, granted: readonly string[]): Promise<PluginSummary>
  update(id: string, change: { readonly enabled?: boolean; readonly granted?: readonly string[] }): Promise<PluginSummary>
  remove(id: string): Promise<void>
  storageGet(id: string): Promise<Record<string, string>>
  /** Applies every change (`null` deletes) in one encrypted write, or none of
   * them. Answers with every value the plugin now has. */
  storageUpdate(id: string, values: Readonly<Record<string, string | null>>): Promise<Record<string, string>>
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
