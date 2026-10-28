import { TOKEN_LIMIT, tokenProblem } from '../../../shared/contracts/tokens'
import { isPluginPermission, permissionSpec, type PluginPermission } from './permissions'
import { checkSettings, type PluginSetting } from './settings'

/**
 * The plugin package format, schema 1: one JSON file that carries the
 * manifest and every asset. The control plane decodes it strictly with the
 * same rules; checking here first turns a refusal into a precise message in
 * the install review instead of a bare `invalid_plugin`.
 */
export const PACKAGE_SCHEMA = 1
export const PLUGIN_ID_PATTERN = /^[a-z0-9][a-z0-9.-]{1,62}[a-z0-9]$/u
const VERSION_PATTERN = /^[0-9A-Za-z.+-]{1,32}$/u
export const NAME_LIMIT = 64
export const AUTHOR_LIMIT = 64
export const DESCRIPTION_LIMIT = 280
export const THEME_NAME_LIMIT = 48
export const CSS_BYTES = 64 * 1024
export const SCRIPT_BYTES = 128 * 1024
/** The shell refuses a command frame over this, envelope included. */
export const FRAME_BYTES = 262_144
/** The control plane refuses a stored plugin whose `plugins.get` answer would
 * be larger than one result payload. */
export const PAYLOAD_BYTES = 258_048
export const PLUGIN_LIMIT = 24
export const HOMEPAGE_BYTES = 200
/** The longest install link the control plane keeps. */
export const SOURCE_BYTES = 2048
/** Permissions one package may list. */
export const PERMISSION_LIMIT = 24
/** Hosts in `network.hosts`. */
export const NETWORK_HOST_LIMIT = 16
const HOST_BYTES = 253
export { SECRET_SETTING_LIMIT, SECRET_VALUE_LIMIT } from './settings'
const APP_VERSION_PATTERN = /^\d{1,4}\.\d{1,4}\.\d{1,4}$/u
const HOST_LABEL = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/u
/** Names that only ever mean this machine or the local network. */
const PRIVATE_SUFFIXES = ['localhost', 'local', 'internal'] as const

const PACKAGE_FIELDS = new Set(['schema', 'id', 'name', 'version', 'author', 'description', 'homepage', 'minAppVersion', 'permissions', 'network', 'settings', 'theme', 'css', 'script'])
// Go's unicode.IsSpace or unicode.IsControl.
// eslint-disable-next-line no-control-regex -- control characters are exactly what this refuses
const SPACE_OR_CONTROL = /[\p{White_Space}\u0000-\u001f\u007f-\u009f]/u
const THEME_FIELDS = new Set(['name', 'scheme', 'tokens'])
// Go's unicode.IsControl: C0, DEL and C1.
// eslint-disable-next-line no-control-regex -- control characters are exactly what this refuses
const CONTROL = /[\u0000-\u001f\u007f-\u009f]/u
// eslint-disable-next-line no-control-regex -- control characters are exactly what this refuses
const CONTROL_EXCEPT_NEWLINE = /[\u0000-\u0009\u000b-\u001f\u007f-\u009f]/u
const CSS_FORBIDDEN = /@import|<\/style/iu

export interface PluginTheme {
  readonly name: string
  readonly scheme: 'dark' | 'light'
  readonly tokens: Readonly<Record<string, string>>
}

export interface PluginNetwork {
  /** Lowercase DNS names; a leading `*.` allows every subdomain (not the name itself). */
  readonly hosts: readonly string[]
}

export interface PluginPackage {
  readonly schema: typeof PACKAGE_SCHEMA
  readonly id: string
  readonly name: string
  readonly version: string
  readonly author?: string
  readonly description?: string
  /** An https:// page about the plugin; shown as a link, never fetched. */
  readonly homepage?: string
  /** The oldest Luxury Switchboard it runs on, `major.minor.patch`. */
  readonly minAppVersion?: string
  readonly permissions: readonly PluginPermission[]
  /** The only hosts `net.fetch` may reach; present exactly when `net:fetch` is listed. */
  readonly network?: PluginNetwork
  /** Options the operator edits on the Plugins page; the script reads them. */
  readonly settings?: readonly PluginSetting[]
  readonly theme?: PluginTheme
  readonly css?: string
  readonly script?: string
}

export type PackageCheck =
  | { readonly ok: true; readonly package: PluginPackage }
  | { readonly ok: false; readonly problems: readonly string[] }

const encoder = new TextEncoder()
export const byteLength = (value: string): number => encoder.encode(value).length
const runeLength = (value: string): number => [...value].length

/** Reads a package file's text. Every problem is reported, not just the
 * first, so a plugin author fixes the file in one pass. */
export function parsePackage(text: string): PackageCheck {
  let value: unknown
  try {
    value = JSON.parse(text)
  } catch {
    return { ok: false, problems: ['The file is not valid JSON.'] }
  }
  return checkPackage(value)
}

export function checkPackage(value: unknown): PackageCheck {
  const problems: string[] = []
  if (!isRecord(value)) return { ok: false, problems: ['A plugin package is a JSON object.'] }

  for (const key of Object.keys(value)) if (!PACKAGE_FIELDS.has(key)) problems.push(`Unknown field "${key}".`)
  if (value.schema !== PACKAGE_SCHEMA) problems.push(`"schema" must be ${PACKAGE_SCHEMA}.`)

  const id = value.id
  if (typeof id !== 'string' || !PLUGIN_ID_PATTERN.test(id)) {
    problems.push('"id" must be 3–64 characters of lowercase letters, digits, dots and dashes, starting and ending with a letter or digit.')
  }

  const name = value.name
  if (typeof name !== 'string' || !name.trim()) problems.push('"name" is required.')
  else if (runeLength(name) > NAME_LIMIT) problems.push(`"name" is longer than ${NAME_LIMIT} characters.`)
  else if (CONTROL.test(name)) problems.push('"name" contains control characters.')

  const version = value.version
  if (typeof version !== 'string' || !VERSION_PATTERN.test(version)) {
    problems.push('"version" must be 1–32 characters of letters, digits, dots, plus and minus signs (e.g. "1.2.0").')
  }

  if (value.author !== undefined) {
    if (typeof value.author !== 'string') problems.push('"author" must be a string.')
    else if (runeLength(value.author) > AUTHOR_LIMIT) problems.push(`"author" is longer than ${AUTHOR_LIMIT} characters.`)
    else if (CONTROL.test(value.author)) problems.push('"author" contains control characters.')
  }

  if (value.description !== undefined) {
    if (typeof value.description !== 'string') problems.push('"description" must be a string.')
    else if (runeLength(value.description) > DESCRIPTION_LIMIT) problems.push(`"description" is longer than ${DESCRIPTION_LIMIT} characters.`)
    else if (CONTROL_EXCEPT_NEWLINE.test(value.description)) problems.push('"description" contains control characters other than line breaks.')
  }

  if (value.homepage !== undefined && (typeof value.homepage !== 'string' || (value.homepage !== '' && !isHomepage(value.homepage)))) {
    problems.push(`"homepage" must be an https:// address of at most ${HOMEPAGE_BYTES} characters.`)
  }

  if (value.minAppVersion !== undefined && (typeof value.minAppVersion !== 'string' || !APP_VERSION_PATTERN.test(value.minAppVersion))) {
    problems.push('"minAppVersion" must be a version such as "1.0.65".')
  }

  const permissions: PluginPermission[] = []
  if (!Array.isArray(value.permissions)) {
    problems.push('"permissions" must be a list (it may be empty).')
  } else if (value.permissions.length > PERMISSION_LIMIT) {
    problems.push(`"permissions" lists more than ${PERMISSION_LIMIT} entries.`)
  } else {
    for (const permission of value.permissions) {
      if (!isPluginPermission(permission)) problems.push(`Unknown permission ${JSON.stringify(permission)}.`)
      else if (permissions.includes(permission)) problems.push(`Permission "${permission}" is listed twice.`)
      else permissions.push(permission)
    }
  }

  let network: PluginNetwork | undefined
  if (value.network !== undefined) {
    const checked = checkNetwork(value.network, problems)
    if (checked) network = checked
    if (!permissions.includes('net:fetch')) problems.push('"network" applies only with the "net:fetch" permission.')
  } else if (permissions.includes('net:fetch')) {
    problems.push('"net:fetch" needs "network": { "hosts": [...] } naming the hosts it may reach.')
  }

  let theme: PluginTheme | undefined
  if (value.theme !== undefined) {
    const checked = checkTheme(value.theme, problems)
    if (checked) theme = checked
    if (!permissions.includes('ui:theme')) problems.push('A theme needs the "ui:theme" permission.')
  }

  if (value.css !== undefined) {
    if (typeof value.css !== 'string') problems.push('"css" must be a string.')
    else {
      if (byteLength(value.css) > CSS_BYTES) problems.push(`"css" is larger than ${CSS_BYTES / 1024} KiB.`)
      if (CSS_FORBIDDEN.test(value.css)) problems.push('"css" may not contain @import or </style>.')
      if (!permissions.includes('ui:css')) problems.push('A stylesheet needs the "ui:css" permission.')
    }
  }

  let script = ''
  if (value.script !== undefined) {
    if (typeof value.script !== 'string') problems.push('"script" must be a string.')
    else {
      script = value.script
      if (byteLength(script) > SCRIPT_BYTES) problems.push(`"script" is larger than ${SCRIPT_BYTES / 1024} KiB.`)
    }
  }
  const scripted = permissions.filter((permission) => permissionSpec(permission).needsScript)
  if (scripted.length > 0 && !script.trim()) {
    problems.push(`${scripted.map((permission) => `"${permission}"`).join(', ')} ${scripted.length === 1 ? 'needs' : 'need'} a "script".`)
  }

  let settings: PluginSetting[] | null = null
  if (value.settings !== undefined) {
    settings = checkSettings(value.settings, problems)
    if (Array.isArray(value.settings) && value.settings.length > 0 && !script.trim()) problems.push('"settings" need a "script" to read them.')
  }

  if (problems.length > 0) return { ok: false, problems }
  const result: PluginPackage = {
    schema: PACKAGE_SCHEMA,
    id: id as string,
    name: name as string,
    version: version as string,
    ...(value.author !== undefined ? { author: value.author as string } : {}),
    ...(value.description !== undefined ? { description: value.description as string } : {}),
    ...(value.homepage !== undefined ? { homepage: value.homepage as string } : {}),
    ...(value.minAppVersion !== undefined ? { minAppVersion: value.minAppVersion as string } : {}),
    permissions,
    ...(network ? { network } : {}),
    ...(settings ? { settings } : {}),
    ...(theme ? { theme } : {}),
    ...(value.css !== undefined ? { css: value.css as string } : {}),
    ...(value.script !== undefined ? { script } : {}),
  }
  return { ok: true, package: result }
}

function checkNetwork(raw: unknown, problems: string[]): PluginNetwork | null {
  if (!isRecord(raw)) {
    problems.push('"network" must be an object with "hosts".')
    return null
  }
  const before = problems.length
  for (const key of Object.keys(raw)) if (key !== 'hosts') problems.push(`Unknown field "network.${key}".`)
  const hosts: string[] = []
  if (!Array.isArray(raw.hosts) || raw.hosts.length === 0 || raw.hosts.length > NETWORK_HOST_LIMIT) {
    problems.push(`"network.hosts" must list 1 to ${NETWORK_HOST_LIMIT} hosts.`)
  } else {
    raw.hosts.forEach((host, index) => {
      const problem = hostProblem(host)
      if (problem) problems.push(`"network.hosts[${index}]" ${problem}.`)
      else if (hosts.includes(host as string)) problems.push(`"network.hosts[${index}]" repeats "${host as string}".`)
      else hosts.push(host as string)
    })
  }
  return problems.length > before ? null : { hosts }
}

/** Why a `network.hosts` entry is refused, or ''. The control plane applies
 * the same rules; the address a name resolves to is checked again when the
 * request connects. */
export function hostProblem(host: unknown): string {
  if (typeof host !== 'string' || !host) return 'must be a host name'
  if (host.length > HOST_BYTES) return `is longer than ${HOST_BYTES} characters`
  const name = host.startsWith('*.') ? host.slice(2) : host
  if (/[:/?#@\s]/u.test(name)) return 'must be a bare host name, without a scheme, port or path'
  const labels = name.split('.')
  if (!labels.every((label) => HOST_LABEL.test(label))) return 'must be a lowercase DNS name such as "api.example.com" or "*.example.com"'
  const last = labels[labels.length - 1]
  if (PRIVATE_SUFFIXES.some((suffix) => suffix === last)) return 'names this machine or the local network, which plugins cannot reach'
  // No top-level domain is all digits, so this also catches every IPv4 literal.
  if (/^\d+$/u.test(last)) return 'must be a name, not an IP address'
  if (labels.length < 2) return 'must have a domain, such as "example.com"'
  return ''
}

function checkTheme(raw: unknown, problems: string[]): PluginTheme | null {
  if (!isRecord(raw)) {
    problems.push('"theme" must be an object with name, scheme and tokens.')
    return null
  }
  const before = problems.length
  for (const key of Object.keys(raw)) if (!THEME_FIELDS.has(key)) problems.push(`Unknown field "theme.${key}".`)
  if (typeof raw.name !== 'string' || !raw.name.trim()) problems.push('"theme.name" is required.')
  else if (runeLength(raw.name) > THEME_NAME_LIMIT) problems.push(`"theme.name" is longer than ${THEME_NAME_LIMIT} characters.`)
  else if (CONTROL.test(raw.name)) problems.push('"theme.name" contains control characters.')
  if (raw.scheme !== 'dark' && raw.scheme !== 'light') problems.push('"theme.scheme" must be "dark" or "light".')
  const tokens: Record<string, string> = {}
  if (!isRecord(raw.tokens)) {
    problems.push('"theme.tokens" must be an object of custom properties.')
  } else {
    const entries = Object.entries(raw.tokens)
    if (entries.length > TOKEN_LIMIT) problems.push(`"theme.tokens" has more than ${TOKEN_LIMIT} entries.`)
    for (const [name, value] of entries.slice(0, TOKEN_LIMIT + 1)) {
      if (typeof value !== 'string') { problems.push(`Token ${name} must be a string.`); continue }
      const problem = tokenProblem(name, value) || (CONTROL.test(value) ? `${name} contains control characters` : '')
      if (problem) problems.push(`${problem}.`)
      else tokens[name] = value
    }
  }
  if (problems.length > before) return null
  return { name: raw.name as string, scheme: raw.scheme as 'dark' | 'light', tokens }
}

/**
 * Whether the package fits the two byte budgets on its way in: the shell's
 * command frame (envelope included) and the control plane's answer when the
 * runtime asks for it back. Returns a problem or ''.
 */
export function packageSizeProblem(pkg: PluginPackage, granted: readonly string[]): string {
  const frame = JSON.stringify({ v: 1, id: `req_${'0'.repeat(32)}`, type: 'command', method: 'plugins.install', payload: { package: pkg, granted } })
  if (byteLength(frame) > FRAME_BYTES) return `This package is ${formatKiB(byteLength(frame))} on the wire; one command carries at most ${FRAME_BYTES / 1024} KiB.`
  const answer = JSON.stringify({
    plugin: {
      id: pkg.id, name: pkg.name, version: pkg.version, author: pkg.author ?? '', description: pkg.description ?? '',
      homepage: pkg.homepage ?? '', minAppVersion: pkg.minAppVersion ?? '',
      permissions: pkg.permissions, granted: pkg.permissions, enabled: true,
      installedAt: '2026-01-01T00:00:00.000Z', updatedAt: '2026-01-01T00:00:00.000Z', sizeBytes: 999_999,
      hasScript: true, hasCss: true, hasTheme: true, hasSettings: true, problem: '',
      secrets: (pkg.settings ?? []).filter((setting) => setting.type === 'secret').map((setting) => setting.key),
      hosts: pkg.network?.hosts ?? [],
    },
    script: pkg.script ?? '', css: pkg.css ?? '', theme: pkg.theme ?? null, settings: pkg.settings ?? [],
    ...(pkg.network ? { network: pkg.network } : {}),
  })
  if (byteLength(answer) > PAYLOAD_BYTES) return `This package is too large to load back (${formatKiB(byteLength(answer))}; at most ${Math.floor(PAYLOAD_BYTES / 1024)} KiB).`
  return ''
}

/** Whether value is an address the homepage link may open: the same rules
 * the control plane applies. `limit` lets a longer install link pass the
 * same checks. */
export function isHomepage(value: string, limit = HOMEPAGE_BYTES): boolean {
  if (byteLength(value) > limit || SPACE_OR_CONTROL.test(value) || !value.startsWith('https://')) return false
  const rest = value.slice('https://'.length)
  const end = rest.search(/[/?#]/u)
  // A userinfo part, even an empty one the URL parser would drop.
  if ((end === -1 ? rest : rest.slice(0, end)).includes('@')) return false
  try {
    return new URL(value).hostname !== ''
  } catch {
    return false
  }
}

/** Whether the running app is older than a package's minAppVersion. An
 * unknown running version (an old control plane) is not held against it. */
export function needsNewerApp(minAppVersion: string | undefined, appVersion: string): boolean {
  if (!minAppVersion) return false
  const need = versionParts(minAppVersion)
  const have = versionParts(appVersion)
  if (!need || !have) return false
  for (let index = 0; index < 3; index += 1) {
    if (have[index] !== need[index]) return have[index] < need[index]
  }
  return false
}

function versionParts(version: string): [number, number, number] | null {
  const match = /^(\d{1,4})\.(\d{1,4})\.(\d{1,4})/u.exec(version)
  return match ? [Number(match[1]), Number(match[2]), Number(match[3])] : null
}

/** Orders two package versions semver-style: numeric dot parts (missing
 * parts count as 0), a `-prerelease` before its release, `+build` ignored.
 * Null when either is not numbered (`nightly`), so no direction is claimed. */
export function compareVersions(a: string, b: string): -1 | 0 | 1 | null {
  const left = versionShape(a)
  const right = versionShape(b)
  if (!left || !right) return null
  for (let index = 0; index < Math.max(left.core.length, right.core.length); index += 1) {
    const order = sign((left.core[index] ?? 0) - (right.core[index] ?? 0))
    if (order) return order
  }
  if (!left.pre.length || !right.pre.length) return sign(right.pre.length - left.pre.length)
  for (let index = 0; index < Math.max(left.pre.length, right.pre.length); index += 1) {
    const x = left.pre[index]
    const y = right.pre[index]
    if (x === undefined || y === undefined) return x === undefined ? -1 : 1
    const xNumber = /^\d+$/u.test(x)
    const yNumber = /^\d+$/u.test(y)
    const order = xNumber && yNumber ? sign(Number(x) - Number(y)) : xNumber !== yNumber ? (xNumber ? -1 : 1) : x < y ? -1 : x > y ? 1 : 0
    if (order) return order
  }
  return 0
}

function versionShape(version: string): { core: number[]; pre: string[] } | null {
  const [main = ''] = version.split('+', 1)
  const dash = main.indexOf('-')
  const core = dash < 0 ? main : main.slice(0, dash)
  if (!/^\d+(\.\d+)*$/u.test(core)) return null
  const pre = dash < 0 ? [] : main.slice(dash + 1).split('.')
  if (pre.some((part) => part === '')) return null
  return { core: core.split('.').map(Number), pre }
}

const sign = (value: number): -1 | 0 | 1 => (value < 0 ? -1 : value > 0 ? 1 : 0)

/** The byte size the control plane will report for the stored package. */
export function packageBytes(pkg: PluginPackage): number {
  return byteLength(JSON.stringify(pkg))
}

function formatKiB(bytes: number): string {
  return `${(bytes / 1024).toFixed(1)} KiB`
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
