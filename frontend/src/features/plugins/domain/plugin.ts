import { isHomepage, type PluginTheme } from './manifest'
import { checkSettings, type PluginSetting } from './settings'

/** An installed plugin as the control plane lists it. Every field is always
 * present: `author`/`description`/`homepage`/`minAppVersion` are empty
 * strings when the package left them out. */
export interface PluginSummary {
  readonly id: string
  readonly name: string
  readonly version: string
  readonly author: string
  readonly description: string
  /** A checked https:// address, or ''. */
  readonly homepage: string
  readonly minAppVersion: string
  /** What the package declares, in its own order. */
  readonly permissions: readonly string[]
  /** What the operator granted; a subset of `permissions`. */
  readonly granted: readonly string[]
  readonly enabled: boolean
  /** RFC 3339, UTC, 0–3 fraction digits (`…07Z`, `…07.25Z`). A record whose
   * time was lost reads `0001-01-01T00:00:00Z`; see `knownTime`. */
  readonly installedAt: string
  /** Moves forward on every install and upgrade, so it doubles as a code
   * revision. */
  readonly updatedAt: string
  /** Bytes of the stored package's compact JSON. */
  readonly sizeBytes: number
  readonly hasScript: boolean
  readonly hasCss: boolean
  readonly hasTheme: boolean
  readonly hasSettings: boolean
  /** Why the stored plugin cannot run (it fails today's checks, or needs a
   * newer app), or ''. A plugin with a problem stays disabled until it is
   * reinstalled or removed. */
  readonly problem: string
  /** The keys of the `secret` settings that have a value; never the values. */
  readonly secrets: readonly string[]
  /** The package's `network.hosts`; empty without `net:fetch`. */
  readonly hosts: readonly string[]
  /** Set while the plugin is loaded from a folder for development. */
  readonly dev?: PluginDev
}

/** A plugin linked from a local folder: the control plane re-reads it there
 * and reloads it when it changes. */
export interface PluginDev {
  readonly path: string
  /** When the folder was last packed into the running version (RFC 3339). */
  readonly loadedAt: string
  /** Why the latest change could not be loaded; the last good version keeps
   * running meanwhile. Empty when it loaded. */
  readonly problem: string
}

/** Everything the runtime needs to run one plugin. */
export interface PluginDetail {
  readonly plugin: PluginSummary
  /** Empty when the package has none. */
  readonly script: string
  readonly css: string
  readonly theme: PluginTheme | null
  readonly settings: readonly PluginSetting[]
}

/**
 * - `disabled`: switched off by the operator.
 * - `active`: enabled, no script — its theme or stylesheet is applied.
 * - `starting`: the script is loading and has not said it is ready.
 * - `running`: the script is live.
 * - `crashed`: the script failed or broke a rule and was stopped.
 * - `stopped`: enabled but not started (safe mode, or not loaded yet).
 */
export type RuntimeState = 'disabled' | 'active' | 'starting' | 'running' | 'crashed' | 'stopped'

export interface LogEntry {
  readonly at: number
  readonly level: 'debug' | 'info' | 'warn' | 'error'
  readonly text: string
}

export interface PluginRuntime {
  readonly state: RuntimeState
  /** Why it stopped or crashed, in plain words; empty otherwise. */
  readonly reason: string
  /** Errors thrown inside the plugin's handlers that it survived. */
  readonly faults: number
  /** Its console output and the host's notes about it, newest last, bounded. */
  readonly logs: readonly LogEntry[]
  /** What the running script added to the window. */
  readonly pages: readonly { readonly key: string; readonly title: string }[]
  readonly commands: number
  readonly status: number
}

export const LOG_LIMIT = 200

export const IDLE_RUNTIME: PluginRuntime = { state: 'stopped', reason: '', faults: 0, logs: [], pages: [], commands: 0, status: 0 }

/** The owner key a plugin's contributions live under. */
export function pluginOwner(pluginId: string): string {
  return `plugin:${pluginId}`
}

/** Reads one plugin from the control plane. Throws on a shape the runtime
 * cannot trust, rather than guessing at an id or a grant. */
export function normalizeSummary(raw: unknown): PluginSummary {
  const value = record(raw)
  if (typeof value.id !== 'string' || !value.id) throw new Error('The control plane returned a plugin without an id')
  return {
    id: value.id,
    name: str(value.name) || value.id,
    version: str(value.version),
    author: str(value.author),
    description: str(value.description),
    homepage: typeof value.homepage === 'string' && isHomepage(value.homepage) ? value.homepage : '',
    minAppVersion: str(value.minAppVersion),
    permissions: strings(value.permissions),
    granted: strings(value.granted),
    enabled: value.enabled === true,
    installedAt: str(value.installedAt),
    updatedAt: str(value.updatedAt),
    sizeBytes: typeof value.sizeBytes === 'number' && Number.isFinite(value.sizeBytes) ? value.sizeBytes : 0,
    hasScript: value.hasScript === true,
    hasCss: value.hasCss === true,
    hasTheme: value.hasTheme === true,
    hasSettings: value.hasSettings === true,
    problem: str(value.problem),
    secrets: strings(value.secrets),
    hosts: strings(value.hosts),
    ...devLink(value.dev),
  }
}

function devLink(raw: unknown): { dev?: PluginDev } {
  const value = record(raw)
  if (typeof value.path !== 'string' || !value.path) return {}
  return { dev: { path: value.path, loadedAt: str(value.loadedAt), problem: str(value.problem) } }
}

/** A stored time as milliseconds, or null when it is missing, unreadable or
 * the zero time a broken record reports. */
export function knownTime(value: string): number | null {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/u.test(value)) return null
  const at = Date.parse(value)
  return Number.isFinite(at) && at > 0 ? at : null
}

export function normalizeList(raw: unknown): PluginSummary[] {
  const plugins = record(raw).plugins
  return Array.isArray(plugins) ? plugins.map(normalizeSummary) : []
}

export function normalizeDetail(raw: unknown): PluginDetail {
  const value = record(raw)
  let theme: PluginTheme | null = null
  if (value.theme && typeof value.theme === 'object') {
    const t = record(value.theme)
    const tokens: Record<string, string> = {}
    for (const [name, token] of Object.entries(record(t.tokens))) if (typeof token === 'string') tokens[name] = token
    theme = { name: str(t.name) || 'Plugin theme', scheme: t.scheme === 'light' ? 'light' : 'dark', tokens }
  }
  // Checked again with the package rules: a declaration the shell cannot
  // trust gives the plugin no settings rather than half of them.
  const problems: string[] = []
  const settings = value.settings === undefined || value.settings === null ? [] : checkSettings(value.settings, problems) ?? []
  return { plugin: normalizeSummary(value.plugin), script: str(value.script), css: str(value.css), theme, settings }
}

export function normalizeValues(raw: unknown): Record<string, string> {
  const values: Record<string, string> = {}
  for (const [key, value] of Object.entries(record(record(raw).values))) if (typeof value === 'string') values[key] = value
  return values
}

function record(value: unknown): Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : {}
}

function str(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

export function strings(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((entry): entry is string => typeof entry === 'string') : []
}

/** A page's key and route tail: `<pluginId>/<pageId>`. */
export function pageKey(pluginId: string, pageId: string): string {
  return `${pluginId}/${pageId}`
}

/** `<pluginId>/<pageId>` → its two halves, or null for anything else. */
export function splitPageKey(key: string): { pluginId: string; pageId: string } | null {
  const slash = key.indexOf('/')
  if (slash <= 0 || slash === key.length - 1 || key.indexOf('/', slash + 1) !== -1) return null
  return { pluginId: key.slice(0, slash), pageId: key.slice(slash + 1) }
}
