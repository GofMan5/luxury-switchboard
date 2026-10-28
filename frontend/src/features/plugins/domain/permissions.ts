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
  'ui:clipboard',
  'control:read',
  'control:write',
  'control:sensitive',
  'storage',
  'ui:slots',
  'ui:layout',
  'ui:dialogs',
  'ui:sound',
  'files',
  'bus',
  'net:fetch',
  'relay:rules',
  'relay:content',
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
  { id: 'ui:notify', title: 'Notifications', description: 'Raises notifications in the feed and as toasts, and asks you to confirm its own actions.', risk: 'low', grantedByDefault: true, needsScript: true },
  { id: 'ui:clipboard', title: 'Copy to clipboard', description: 'Puts text on your clipboard when you press one of its buttons or run one of its commands. It can never read the clipboard.', risk: 'low', grantedByDefault: true, needsScript: true },
  { id: 'control:read', title: 'Read relay state', description: 'Reads providers, routes, keys metadata, live activity, history, analytics and settings, and follows their live updates. Credentials stay write-only.', risk: 'medium', grantedByDefault: true, needsScript: true },
  { id: 'control:write', title: 'Change relay state', description: 'Starts and stops the relay, switches the active provider, edits routes, runs model tests and clears findings — anything you can do on those pages except credentials.', risk: 'high', grantedByDefault: true, needsScript: true },
  { id: 'control:sensitive', title: 'Credentials and exposure', description: 'Adds, edits and removes providers and API keys, changes settings, controls the public tunnel and reveals its token, and signs Codex accounts in or out. Grant only to plugins you wrote or fully trust.', risk: 'high', grantedByDefault: false, needsScript: true },
  { id: 'storage', title: 'Storage', description: 'Keeps its own data on this machine: up to 64 KiB of settings-like values and 2 MiB of documents. It cannot read another plugin’s data.', risk: 'low', grantedByDefault: true, needsScript: true },
  { id: 'ui:slots', title: 'Widgets and actions', description: 'Adds Overview widgets, inspector tabs, a Settings section, buttons in the inspectors and on API key rows, and icon buttons in the top bar. Each one is labelled with the plugin’s name.', risk: 'low', grantedByDefault: true, needsScript: true },
  { id: 'ui:layout', title: 'Sidebar layout', description: 'Hides or reorders built-in sidebar pages and picks the start page. Plugins and Settings always stay, and you can reset the layout on this page.', risk: 'low', grantedByDefault: true, needsScript: true },
  { id: 'ui:dialogs', title: 'Dialogs', description: 'Opens its own dialogs, such as forms and wizards, right after you use the plugin.', risk: 'low', grantedByDefault: true, needsScript: true },
  { id: 'ui:sound', title: 'Sounds', description: 'Plays short, quiet tones.', risk: 'low', grantedByDefault: true, needsScript: true },
  { id: 'files', title: 'Open and save files', description: 'Reads a text file you pick, and offers files to save, only right after you use the plugin. It never sees your folders.', risk: 'medium', grantedByDefault: true, needsScript: true },
  { id: 'bus', title: 'Talk to other plugins', description: 'Sends and receives messages on named channels shared with your other plugins.', risk: 'low', grantedByDefault: true, needsScript: true },
  { id: 'net:fetch', title: 'Internet access', description: 'Sends HTTPS requests from this machine to the hosts the package lists, using secrets you enter for it. Anything it can read may leave the machine. Grant only to plugins you trust.', risk: 'high', grantedByDefault: false, needsScript: true },
  { id: 'relay:rules', title: 'Relay rules', description: 'Adds request rules (provider, model, blocks, limits, budgets, headers, tags) and custom retry rules to your local relay. It can change only its own rules; they go with the plugin.', risk: 'high', grantedByDefault: true, needsScript: true },
  { id: 'relay:content', title: 'Prompts and answers', description: 'Reads and can change the prompts and answers that pass through your local relay. Never applies to the public tunnel. Grant only to plugins you wrote or fully trust.', risk: 'high', grantedByDefault: false, needsScript: true },
]

/** Permissions whose review shows a warning, not only a description. */
export const DANGEROUS_PERMISSIONS: ReadonlySet<PluginPermission> = new Set(['control:sensitive', 'net:fetch', 'relay:content'])

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

/** The previous decision behind each permission of an upgrade: `net:fetch`
 * counts as new when the network allowlist gained a host. */
export function reviewedBefore(
  previous: { readonly permissions: readonly string[]; readonly hosts: readonly string[] },
  hosts: readonly string[],
): ReadonlySet<string> {
  const before = new Set(previous.permissions)
  if (hosts.some((host) => !previous.hosts.includes(host))) before.delete('net:fetch')
  return before
}

/** Where the review starts for each permission: a fresh install uses the
 * defaults; an upgrade keeps what the operator decided before and applies
 * the defaults only to permissions the new version adds, or whose reach
 * grew (`net:fetch` with a new host). */
export function initialGrants(
  declared: readonly PluginPermission[],
  previous?: { readonly permissions: readonly string[]; readonly granted: readonly string[]; readonly hosts: readonly string[] },
  hosts: readonly string[] = [],
): PluginPermission[] {
  if (!previous) return defaultGrants(declared)
  const before = reviewedBefore(previous, hosts)
  const granted = new Set(previous.granted)
  const fresh = new Set(defaultGrants(declared))
  return declared.filter((permission) => (before.has(permission) ? granted.has(permission) : fresh.has(permission)))
}

/** What an upgrade takes away or widens, beyond the permissions it adds:
 * the review shows these so a quieter package is visible as such too. */
export function upgradeChanges(
  previous: { readonly permissions: readonly string[]; readonly hosts: readonly string[] },
  next: { readonly permissions: readonly string[]; readonly hosts: readonly string[] },
): { readonly droppedPermissions: string[]; readonly addedHosts: string[]; readonly droppedHosts: string[] } {
  return {
    droppedPermissions: previous.permissions.filter((permission) => isPluginPermission(permission) && !next.permissions.includes(permission)),
    addedHosts: next.hosts.filter((host) => !previous.hosts.includes(host)),
    droppedHosts: previous.hosts.filter((host) => !next.hosts.includes(host)),
  }
}

/** Whether an upgrade asks for anything the operator has not decided on yet:
 * a permission the previous version did not declare, or `net:fetch` reaching
 * a new host. One that does not can reuse the previous grants unreviewed. */
export function upgradeAsksMore(
  previous: { readonly permissions: readonly string[]; readonly hosts: readonly string[] },
  next: { readonly permissions: readonly string[]; readonly hosts: readonly string[] },
): boolean {
  const before = reviewedBefore(previous, next.hosts)
  return next.permissions.some((permission) => isPluginPermission(permission) && !before.has(permission))
}
