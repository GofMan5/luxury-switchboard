// Reading, checking, packing and scaffolding plugin packages. Every rule comes
// from frontend/src through ./domain.mjs; this file only gathers the files and
// reports what the app's own checks say.
import { existsSync, mkdirSync, readdirSync, readFileSync, statSync, writeFileSync } from 'node:fs'
import { basename, dirname, join, resolve } from 'node:path'
import vm from 'node:vm'
import { manifest, settings, tokens, workerSource } from './domain.mjs'

/** A mistake in how the CLI was called rather than in the plugin: exit 2. */
export class UsageError extends Error {}

export const SOURCE_FILES = { manifest: 'plugin.json', script: 'main.js', css: 'style.css' }
export const TEMPLATE_KINDS = ['page', 'command', 'theme']
const TEMPLATES = new URL('../../docs/plugins/templates/', import.meta.url)
const ROOT_PACKAGE = new URL('../../package.json', import.meta.url)

/** The version this checkout builds; `run` and the newer-app note compare with it. */
export function checkoutVersion() {
  return JSON.parse(readFileSync(ROOT_PACKAGE, 'utf8')).version
}

const stripBom = (text) => (text.charCodeAt(0) === 0xfeff ? text.slice(1) : text)

function readText(path) {
  try { return stripBom(readFileSync(path, 'utf8')) } catch (error) { throw new UsageError(`Cannot read ${path}: ${error.message}`) }
}

/**
 * The package a path describes, as unchecked JSON, with the problems found
 * while assembling it. A path is either a packed `.json` file or a source
 * directory: `plugin.json` (the package without script and css), an optional
 * `main.js` (the script) and an optional `style.css` (the stylesheet).
 */
export function loadPackage(path) {
  let stat
  try { stat = statSync(path) } catch { throw new UsageError(`${path} does not exist.`) }
  if (!stat.isDirectory()) {
    const text = readText(path)
    try { return { value: JSON.parse(text), problems: [], source: 'package', scriptName: 'the script' } } catch (error) {
      return { value: undefined, problems: [`The file is not valid JSON: ${error.message}`], source: 'package', scriptName: 'the script' }
    }
  }
  const manifestPath = join(path, SOURCE_FILES.manifest)
  if (!existsSync(manifestPath)) throw new UsageError(`${path} has no ${SOURCE_FILES.manifest}; pass a packed .json file or a plugin source directory.`)
  let value
  try { value = JSON.parse(readText(manifestPath)) } catch (error) {
    return { value: undefined, problems: [`${SOURCE_FILES.manifest} is not valid JSON: ${error.message}`], source: 'directory', scriptName: SOURCE_FILES.script }
  }
  const problems = []
  if (value && typeof value === 'object' && !Array.isArray(value)) {
    if ('script' in value) problems.push(`${SOURCE_FILES.manifest} may not carry "script"; the script is ${SOURCE_FILES.script}.`)
    if ('css' in value) problems.push(`${SOURCE_FILES.manifest} may not carry "css"; the stylesheet is ${SOURCE_FILES.css}.`)
    if ('$schema' in value) problems.push(`${SOURCE_FILES.manifest} may not carry "$schema" (packages refuse unknown fields); point your editor at docs/plugins/plugin.schema.json instead.`)
    value = Object.fromEntries(Object.entries(value).filter(([key]) => key !== 'script' && key !== 'css' && key !== '$schema'))
    const scriptPath = join(path, SOURCE_FILES.script)
    const cssPath = join(path, SOURCE_FILES.css)
    if (existsSync(scriptPath)) value.script = readText(scriptPath)
    if (existsSync(cssPath)) value.css = readText(cssPath)
  }
  return { value, problems, source: 'directory', scriptName: SOURCE_FILES.script }
}

/** Compiles the script exactly as the worker will (inside the prelude) without
 * running it. Returns a problem or ''. */
export function compileProblem(script, scriptName = 'the script') {
  const context = { plugin: { id: 'compile.check', name: 'Compile check', version: '0', granted: [] }, app: { version: '0.0.0', capabilities: [] }, settings: {} }
  const { text, offset } = workerSource.workerSource(script, context)
  try {
    new vm.Script(text, { filename: 'plugin.js' })
    return ''
  } catch (error) {
    const match = typeof error?.stack === 'string' ? /plugin\.js:(\d+)/u.exec(error.stack) : null
    const line = match ? Number(match[1]) - offset : 0
    return `${scriptName}${line > 0 ? ` line ${line}` : ''}: ${error?.name ?? 'Error'}: ${error?.message ?? String(error)}`
  }
}

/**
 * Every problem the app would report for this package, plus a compile check
 * of the script and the two byte budgets on the way in. `needsNewerApp` is a
 * note, not a problem: the package is fine, this app is old.
 */
export function validatePackage(loaded, { appVersion = checkoutVersion() } = {}) {
  const { value } = loaded
  const problems = [...loaded.problems]
  const notes = []
  let pkg = null
  if (value !== undefined) {
    const checked = manifest.checkPackage(value)
    if (checked.ok) pkg = checked.package
    else problems.push(...checked.problems)
  }
  const record = value && typeof value === 'object' && !Array.isArray(value) ? value : {}
  const script = typeof record.script === 'string' ? record.script : ''
  const css = typeof record.css === 'string' ? record.css : ''
  if (script.trim()) {
    const problem = compileProblem(script, loaded.scriptName)
    if (problem) problems.push(problem)
  }
  if (pkg) {
    const size = manifest.packageSizeProblem(pkg, pkg.permissions)
    if (size) problems.push(size)
    if (manifest.needsNewerApp(pkg.minAppVersion, appVersion)) notes.push(`Needs Luxury Switchboard ${pkg.minAppVersion} or newer; this checkout is ${appVersion}.`)
  }
  const theme = record.theme && typeof record.theme === 'object' ? record.theme : null
  const tokenCount = theme && theme.tokens && typeof theme.tokens === 'object' ? Object.keys(theme.tokens).length : 0
  const sizes = {
    scriptBytes: manifest.byteLength(script), scriptLimit: manifest.SCRIPT_BYTES,
    cssBytes: manifest.byteLength(css), cssLimit: manifest.CSS_BYTES,
    tokens: tokenCount, tokenLimit: tokens.TOKEN_LIMIT,
    settings: Array.isArray(record.settings) ? record.settings.length : 0, settingLimit: settings.SETTING_LIMIT,
    packageBytes: value === undefined ? 0 : manifest.byteLength(JSON.stringify(value)),
  }
  return {
    ok: problems.length === 0,
    problems,
    notes,
    id: typeof record.id === 'string' ? record.id : '',
    name: typeof record.name === 'string' ? record.name : '',
    version: typeof record.version === 'string' ? record.version : '',
    sizes,
    package: problems.length === 0 ? pkg : null,
    value,
  }
}

/** The package as authored, in the app's field order: nothing normalised,
 * nothing dropped, so a pack is exactly what the author wrote. */
export function packedText(report) {
  const ordered = Object.fromEntries(Object.keys(report.package).map((key) => [key, report.value[key]]))
  return `${JSON.stringify(ordered, null, 2)}\n`
}

/** Validates a source directory and writes the package; returns the report and
 * where it went (nothing is written when there are problems). */
export function packDirectory(dir, { out, appVersion } = {}) {
  let stat
  try { stat = statSync(dir) } catch { throw new UsageError(`${dir} does not exist.`) }
  if (!stat.isDirectory()) throw new UsageError(`pack takes a plugin source directory (${SOURCE_FILES.manifest}, ${SOURCE_FILES.script}, ${SOURCE_FILES.css}); ${dir} is a file.`)
  const report = validatePackage(loadPackage(dir), { appVersion })
  if (!report.ok) return { report, out: '' }
  const target = resolve(out ?? join(dir, 'dist', `${report.package.id}-${report.package.version}.json`))
  const text = packedText(report)
  mkdirSync(dirname(target), { recursive: true })
  writeFileSync(target, text)
  return { report, out: target, bytes: manifest.byteLength(text) }
}

/** The files of one template, by name, as text. */
export function templateFiles(kind) {
  if (!TEMPLATE_KINDS.includes(kind)) throw new UsageError(`--kind must be one of ${TEMPLATE_KINDS.join(', ')}.`)
  const dir = new URL(`${kind}/`, TEMPLATES)
  return Object.fromEntries(readdirSync(dir).sort().map((name) => [name, readFileSync(new URL(name, dir), 'utf8')]))
}

/** Scaffolds a plugin source directory from a template. Refuses a directory
 * that already has files; the result always passes validate. */
export function initDirectory(dir, { kind = 'page', id, name } = {}) {
  const target = resolve(dir)
  if (existsSync(target) && (!statSync(target).isDirectory() || readdirSync(target).length > 0)) {
    throw new UsageError(`${dir} already exists and is not empty; init never overwrites files.`)
  }
  const base = basename(target)
  const pluginId = id ?? `acme.${base.toLowerCase().replace(/[^a-z0-9.-]+/gu, '-').replace(/^[^a-z0-9]+|[^a-z0-9]+$/gu, '')}`
  if (!manifest.PLUGIN_ID_PATTERN.test(pluginId)) {
    throw new UsageError(`"${pluginId}" is not a plugin id: 3–64 lowercase letters, digits, dots and dashes, starting and ending with a letter or digit. Pass --id.`)
  }
  const files = templateFiles(kind)
  const json = JSON.parse(files[SOURCE_FILES.manifest])
  // The template's id also appears in its files (a theme-scoped selector).
  for (const file of Object.keys(files)) if (file !== SOURCE_FILES.manifest) files[file] = files[file].replaceAll(json.id, pluginId)
  json.id = pluginId
  json.name = name ?? base
  files[SOURCE_FILES.manifest] = `${JSON.stringify(json, null, 2)}\n`
  const value = { ...json }
  if (files[SOURCE_FILES.script] !== undefined) value.script = files[SOURCE_FILES.script]
  if (files[SOURCE_FILES.css] !== undefined) value.css = files[SOURCE_FILES.css]
  const report = validatePackage({ value, problems: [], scriptName: SOURCE_FILES.script })
  if (!report.ok) throw new UsageError(`The scaffold would not validate: ${report.problems.join(' ')}`)
  mkdirSync(target, { recursive: true })
  for (const [file, text] of Object.entries(files)) writeFileSync(join(target, file), text)
  return { dir: target, id: pluginId, name: json.name, kind, files: Object.keys(files) }
}
