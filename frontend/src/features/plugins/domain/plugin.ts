import type { PluginTheme } from './manifest'

/** An installed plugin as the control plane lists it. Every field is always
 * present: `author`/`description` are empty strings when the package left
 * them out. */
export interface PluginSummary {
  readonly id: string
  readonly name: string
  readonly version: string
  readonly author: string
  readonly description: string
  /** What the package declares, in its own order. */
  readonly permissions: readonly string[]
  /** What the operator granted; a subset of `permissions`. */
  readonly granted: readonly string[]
  readonly enabled: boolean
  /** RFC 3339, UTC, whole seconds. */
  readonly installedAt: string
  /** Changes on install and upgrade only, so it doubles as a code revision. */
  readonly updatedAt: string
  /** Bytes of the stored package's compact JSON. */
  readonly sizeBytes: number
  readonly hasScript: boolean
  readonly hasCss: boolean
  readonly hasTheme: boolean
}

/** Everything the runtime needs to run one plugin. */
export interface PluginDetail {
  readonly plugin: PluginSummary
  /** Empty when the package has none. */
  readonly script: string
  readonly css: string
  readonly theme: PluginTheme | null
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
    permissions: strings(value.permissions),
    granted: strings(value.granted),
    enabled: value.enabled === true,
    installedAt: str(value.installedAt),
    updatedAt: str(value.updatedAt),
    sizeBytes: typeof value.sizeBytes === 'number' && Number.isFinite(value.sizeBytes) ? value.sizeBytes : 0,
    hasScript: value.hasScript === true,
    hasCss: value.hasCss === true,
    hasTheme: value.hasTheme === true,
  }
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
  return { plugin: normalizeSummary(value.plugin), script: str(value.script), css: str(value.css), theme }
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

function strings(value: unknown): string[] {
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
