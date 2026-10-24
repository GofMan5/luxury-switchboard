import type { PluginPermission } from './permissions'

/**
 * Which control-plane commands a plugin may send, and which permission each
 * one needs. This is an explicit allowlist, not a pattern: a command the
 * table does not name is refused, so a newer sidecar's commands stay out of
 * reach until someone decides where they belong.
 */
const READ = [
  'relay.status',
  'providers.list',
  'providers.health',
  'keys.list',
  'activity.list',
  'activity.summary',
  'history.recent',
  'history.stats',
  'analytics.report',
  'analytics.prices.get',
  'settings.get',
  'guardrails.status',
  'guardrails.findings',
  'notifications.list',
  'routes.list',
  'tunnel.get',
  'clients.list',
  'clients.events',
  'codex.status',
  'codex.quota',
  'codex.quota.all',
  'updates.status',
  'appearance.get',
] as const

const WRITE = [
  'relay.start',
  'relay.stop',
  'providers.activate',
  'routes.upsert',
  'routes.upsertMany',
  'routes.delete',
  'routes.remove',
  'models.discover',
  'models.test',
  'keys.check',
  'keys.reset',
  'keys.move',
  'guardrails.clear',
  'notifications.clear',
  'analytics.prices.set',
  'analytics.prices.remove',
  'analytics.prices.setCurrency',
  'clients.profile',
  'codex.wake',
  'updates.check',
  'tunnel.privacy_test',
] as const

const SENSITIVE = [
  'providers.add',
  'providers.update',
  'providers.delete',
  'keys.add',
  'keys.addMany',
  'keys.update',
  'keys.remove',
  'settings.update',
  'tunnel.configure',
  'tunnel.start',
  'tunnel.stop',
  'tunnel.rotate',
  'tunnel.reveal',
  'codex.login.start',
  'codex.login.status',
  'codex.login.cancel',
  'codex.login.device.start',
  'codex.import.json',
  'codex.import.files',
  'codex.logout',
] as const

/** Never reachable from a plugin, whatever it was granted: managing plugins
 * (a plugin could grant itself more), the persisted appearance (the user's
 * own), the handshake and lifecycle, installing an update, and the plaintext
 * backup — the one place credentials leave the store. */
const DENIED_PREFIXES = ['plugins.', 'system.', 'backup.']
const DENIED = new Set(['appearance.update', 'updates.install'])

export const CONTROL_METHODS: Readonly<Record<string, PluginPermission>> = Object.freeze(Object.fromEntries<PluginPermission>([
  ...READ.map((method) => [method, 'control:read'] as const),
  ...WRITE.map((method) => [method, 'control:write'] as const),
  ...SENSITIVE.map((method) => [method, 'control:sensitive'] as const),
]))

export type ControlAccess =
  | { readonly allowed: true; readonly permission: PluginPermission }
  | { readonly allowed: false; readonly code: 'not_allowed' | 'permission_denied'; readonly message: string; readonly permission?: PluginPermission }

export function controlAccess(method: string, granted: ReadonlySet<string>): ControlAccess {
  if (DENIED.has(method) || DENIED_PREFIXES.some((prefix) => method.startsWith(prefix))) {
    return { allowed: false, code: 'not_allowed', message: `"${method}" is never available to plugins.` }
  }
  const permission = Object.hasOwn(CONTROL_METHODS, method) ? CONTROL_METHODS[method] : undefined
  if (!permission) return { allowed: false, code: 'not_allowed', message: `"${method}" is not a command plugins can use.` }
  if (!granted.has(permission)) {
    return { allowed: false, code: 'permission_denied', permission, message: `"${method}" needs the "${permission}" permission, which this plugin was not granted.` }
  }
  return { allowed: true, permission }
}

/** Live topics a plugin may follow with `control:read`. */
export const CONTROL_TOPICS: readonly string[] = [
  'activity.changed',
  'appearance.changed',
  'clients.changed',
  'codex.changed',
  'guardrails.changed',
  'guardrails.finding',
  'keys.changed',
  'models.tested',
  'notifications.raised',
  'providers.changed',
  'providers.health',
  'relay.changed',
  'routes.changed',
  'settings.changed',
  'tunnel.changed',
  'updates.installProgress',
  'updates.stateChanged',
]

const TOPICS = new Set(CONTROL_TOPICS)

export function topicAccess(topic: string, granted: ReadonlySet<string>): ControlAccess {
  if (!TOPICS.has(topic)) return { allowed: false, code: 'not_allowed', message: `"${topic}" is not a topic plugins can follow.` }
  if (!granted.has('control:read')) {
    return { allowed: false, code: 'permission_denied', permission: 'control:read', message: `Following "${topic}" needs the "control:read" permission, which this plugin was not granted.` }
  }
  return { allowed: true, permission: 'control:read' }
}

export function methodsFor(permission: PluginPermission): string[] {
  return Object.entries(CONTROL_METHODS).filter(([, needed]) => needed === permission).map(([method]) => method)
}
