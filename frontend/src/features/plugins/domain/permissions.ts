/**
 * What a plugin may ask for. The names are part of the package format and
 * the control plane checks the same list, so additions only.
 */
export const PLUGIN_PERMISSIONS = [
  'ui:theme',
  'ui:css',
  'ui:pages',
  'ui:status',
  'ui:commands',
  'ui:notify',
  'control:read',
  'control:write',
  'control:sensitive',
  'storage',
] as const

export type PluginPermission = (typeof PLUGIN_PERMISSIONS)[number]

export type PermissionRisk = 'low' | 'medium' | 'high'

export interface PermissionSpec {
  readonly id: PluginPermission
  /** A short label for chips and checkboxes. */
  readonly title: string
  /** What granting it lets the plugin do, in the operator's words. */
  readonly description: string
  readonly risk: PermissionRisk
  /** Whether the install review starts with it ticked. High-impact grants
   * that can expose credentials start unticked: consent is a deliberate act. */
  readonly grantedByDefault: boolean
  /** True when the permission only means something to a running script. */
  readonly needsScript: boolean
}

export const PERMISSION_SPECS: readonly PermissionSpec[] = [
  { id: 'ui:theme', title: 'Theme', description: 'Adds a colour theme you can pick in Appearance. It cannot change anything else.', risk: 'low', grantedByDefault: true, needsScript: false },
  { id: 'ui:css', title: 'Stylesheet', description: 'Restyles the whole window while enabled. A broken stylesheet can hide controls; safe mode (Ctrl+Alt+Shift+S) turns it off.', risk: 'medium', grantedByDefault: true, needsScript: false },
  { id: 'ui:pages', title: 'Pages', description: 'Adds its own pages to the sidebar, built from the Switchboard’s standard controls.', risk: 'low', grantedByDefault: true, needsScript: true },
  { id: 'ui:status', title: 'Status bar', description: 'Shows short readouts in the runtime bar at the top of the window.', risk: 'low', grantedByDefault: true, needsScript: true },
  { id: 'ui:commands', title: 'Commands', description: 'Adds actions to the command palette (Ctrl+K). They run only when you choose them.', risk: 'low', grantedByDefault: true, needsScript: true },
  { id: 'ui:notify', title: 'Notifications', description: 'Raises notifications in the feed and as toasts.', risk: 'low', grantedByDefault: true, needsScript: true },
  { id: 'control:read', title: 'Read relay state', description: 'Reads providers, routes, keys metadata, live activity, history, analytics and settings, and follows their live updates. Credentials stay write-only.', risk: 'medium', grantedByDefault: true, needsScript: true },
  { id: 'control:write', title: 'Change relay state', description: 'Starts and stops the relay, switches the active provider, edits routes, runs model tests and clears findings — anything you can do on those pages except credentials.', risk: 'high', grantedByDefault: true, needsScript: true },
  { id: 'control:sensitive', title: 'Credentials and exposure', description: 'Adds, edits and removes providers and API keys, changes settings, controls the public tunnel and reveals its token, and signs Codex accounts in or out. Grant only to plugins you wrote or fully trust.', risk: 'high', grantedByDefault: false, needsScript: true },
  { id: 'storage', title: 'Storage', description: 'Keeps up to 64 KiB of its own data on this machine. It cannot read another plugin’s data.', risk: 'low', grantedByDefault: true, needsScript: true },
]

const SPEC_BY_ID = new Map(PERMISSION_SPECS.map((spec) => [spec.id, spec]))

export function permissionSpec(permission: PluginPermission): PermissionSpec {
  return SPEC_BY_ID.get(permission) as PermissionSpec
}

export function isPluginPermission(value: unknown): value is PluginPermission {
  return typeof value === 'string' && SPEC_BY_ID.has(value as PluginPermission)
}

/** The permissions a plugin actually holds: what the operator granted and
 * the package still declares, in declared order. An upgrade that drops a
 * permission drops the grant with it; a stale or forged grant outside the
 * declaration is never honoured. */
export function effectiveGrants(declared: readonly string[], granted: readonly string[]): PluginPermission[] {
  const allowed = new Set(granted)
  const seen = new Set<string>()
  const result: PluginPermission[] = []
  for (const permission of declared) {
    if (!isPluginPermission(permission) || !allowed.has(permission) || seen.has(permission)) continue
    seen.add(permission)
    result.push(permission)
  }
  return result
}

/** The grants the install review starts with. */
export function defaultGrants(declared: readonly PluginPermission[]): PluginPermission[] {
  return declared.filter((permission) => permissionSpec(permission).grantedByDefault)
}

/** Where the review starts for each permission: a fresh install uses the
 * defaults; an upgrade keeps what the operator decided before and applies
 * the defaults only to permissions the new version adds. */
export function initialGrants(
  declared: readonly PluginPermission[],
  previous?: { readonly permissions: readonly string[]; readonly granted: readonly string[] },
): PluginPermission[] {
  if (!previous) return defaultGrants(declared)
  const before = new Set(previous.permissions)
  const granted = new Set(previous.granted)
  const fresh = new Set(defaultGrants(declared))
  return declared.filter((permission) => (before.has(permission) ? granted.has(permission) : fresh.has(permission)))
}
