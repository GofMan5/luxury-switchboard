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

/** Request and retry rules. The host stamps the plugin as the actor on every
 * change, so a plugin can change only the rules it owns. */
export const RULES_METHODS = [
  'rules.list',
  'rules.stats',
  'rules.test',
  'rules.recentErrors',
  'rules.saveRequestRule',
  'rules.deleteRequestRule',
  'rules.saveRetryRule',
  'rules.deleteRetryRule',
] as const

/** Never reachable from a plugin, whatever it was granted: managing plugins
 * (a plugin could grant itself more), relay hooks (only the host registers
 * and answers them, on the plugin's behalf), the persisted appearance (the
 * user's own), the handshake and lifecycle, installing an update, the
 * plaintext backup — the one place credentials leave the store — and rule
 * order, which is the operator's. */
const DENIED_PREFIXES = ['plugins.', 'system.', 'backup.', 'relayhooks.', 'rules.reorder']
const DENIED = new Set(['appearance.update', 'updates.install'])

export const CONTROL_METHODS: Readonly<Record<string, PluginPermission>> = Object.freeze(Object.fromEntries<PluginPermission>([
  ...READ.map((method) => [method, 'control:read'] as const),
  ...WRITE.map((method) => [method, 'control:write'] as const),
  ...SENSITIVE.map((method) => [method, 'control:sensitive'] as const),
  ...RULES_METHODS.map((method) => [method, 'relay:rules'] as const),
]))

/** The rule changes; the control plane rejects `actor` on the read-only ones. */
const STAMPED = new Set<string>(['rules.saveRequestRule', 'rules.deleteRequestRule', 'rules.saveRetryRule', 'rules.deleteRetryRule'])

/** Whether the host must stamp `actor: "plugin:<id>"` on this command. */
export function stampsActor(method: string): boolean {
  return STAMPED.has(method)
}

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

/** Live topics a plugin may follow, and the permission each one needs. */
export const TOPIC_PERMISSIONS: Readonly<Record<string, PluginPermission>> = Object.freeze({
  'activity.changed': 'control:read',
  'appearance.changed': 'control:read',
  'clients.changed': 'control:read',
  'codex.changed': 'control:read',
  'guardrails.changed': 'control:read',
  'guardrails.finding': 'control:read',
  'keys.changed': 'control:read',
  'models.tested': 'control:read',
  'notifications.raised': 'control:read',
  'providers.changed': 'control:read',
  'providers.health': 'control:read',
  'relay.changed': 'control:read',
  'routes.changed': 'control:read',
  'rules.changed': 'relay:rules',
  'settings.changed': 'control:read',
  'tunnel.changed': 'control:read',
  'updates.installProgress': 'control:read',
  'updates.stateChanged': 'control:read',
})

export const CONTROL_TOPICS: readonly string[] = Object.keys(TOPIC_PERMISSIONS)

export function topicAccess(topic: string, granted: ReadonlySet<string>): ControlAccess {
  const permission = Object.hasOwn(TOPIC_PERMISSIONS, topic) ? TOPIC_PERMISSIONS[topic] : undefined
  if (!permission) return { allowed: false, code: 'not_allowed', message: `"${topic}" is not a topic plugins can follow.` }
  if (!granted.has(permission)) {
    return { allowed: false, code: 'permission_denied', permission, message: `Following "${topic}" needs the "${permission}" permission, which this plugin was not granted.` }
  }
  return { allowed: true, permission }
}

export function topicsFor(permission: PluginPermission): string[] {
  return Object.entries(TOPIC_PERMISSIONS).filter(([, needed]) => needed === permission).map(([topic]) => topic)
}

export function methodsFor(permission: PluginPermission): string[] {
  return Object.entries(CONTROL_METHODS).filter(([, needed]) => needed === permission).map(([method]) => method)
}

/** Where a provider sends traffic and how it authenticates: owner-only
 * metadata (AGENTS: upstream URLs stay on the Providers screen). */
export const UPSTREAM_FIELDS: ReadonlySet<string> = new Set(['baseUrl', 'authHeader', 'modelsPath', 'chatPath'])

/** A control-plane answer or event as a plugin without `control:sensitive`
 * may see it: every upstream field removed, wherever it sits. */
export function redactUpstream(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(redactUpstream)
  if (value === null || typeof value !== 'object') return value
  return Object.fromEntries(Object.entries(value).filter(([key]) => !UPSTREAM_FIELDS.has(key)).map(([key, item]) => [key, redactUpstream(item)]))
}
