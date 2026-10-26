// Tests for `pnpm plugin` (scripts/plugin.mjs). Run: pnpm plugin:test
//
// Everything here goes through the frontend's own plugin domain, loaded from
// frontend/src, so a rule change there is a rule change here.
import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { after, describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { manifest, permissions, settings, tokens } from './plugin/domain.mjs'
import { initDirectory, loadPackage, packDirectory, packedText, TEMPLATE_KINDS, UsageError, validatePackage } from './plugin/package.mjs'
import { runPackage, tidyStack } from './plugin/run.mjs'

const ROOT = fileURLToPath(new URL('..', import.meta.url))
const CLI = join(ROOT, 'scripts', 'plugin.mjs')
const EXAMPLES = join(ROOT, 'docs', 'plugins', 'examples')
const SCHEMA = JSON.parse(readFileSync(join(ROOT, 'docs', 'plugins', 'plugin.schema.json'), 'utf8'))
const LATENCY_BOARD = join(EXAMPLES, 'latency-board.json')
/** Source-folder examples (docs/plugins/examples/<name>/plugin.json); the top-level .json files are the built-in ones. */
const SOURCE_EXAMPLES = readdirSync(EXAMPLES, { withFileTypes: true }).filter((entry) => entry.isDirectory()).map((entry) => entry.name).sort()

const scratch = mkdtempSync(join(tmpdir(), 'switchboard-plugin-test-'))
after(() => rmSync(scratch, { recursive: true, force: true }))
let dirs = 0
const freshDir = (name = 'p') => join(scratch, `${++dirs}-${name}`)

const cli = (...args) => {
  const result = spawnSync(process.execPath, [CLI, ...args], { cwd: ROOT, encoding: 'utf8', timeout: 60_000 })
  return { code: result.status, stdout: result.stdout, stderr: result.stderr }
}

/** A source directory from a template, with files replaced or added. */
function sourceDir(kind, files = {}, options = {}) {
  const dir = freshDir(kind)
  initDirectory(dir, { kind, ...options })
  for (const [file, text] of Object.entries(files)) writeFileSync(join(dir, file), text)
  return dir
}

function checked(path) {
  const report = validatePackage(loadPackage(path))
  assert.deepEqual(report.problems, [], `${path} should validate`)
  return report.package
}

/** Every node in a UI tree, depth first. */
function* nodes(tree) {
  if (Array.isArray(tree)) for (const item of tree) yield* nodes(item)
  else if (tree && typeof tree === 'object') {
    yield tree
    for (const value of Object.values(tree)) if (value && typeof value === 'object') yield* nodes(value)
  }
}

describe('validate', () => {
  for (const file of [...readdirSync(EXAMPLES).filter((name) => name.endsWith('.json')).sort(), ...SOURCE_EXAMPLES]) {
    test(`examples/${file} is valid`, () => {
      const report = validatePackage(loadPackage(join(EXAMPLES, file)))
      assert.equal(report.ok, true, report.problems.join('\n'))
      assert.deepEqual(report.problems, [])
    })
  }

  test('reports every problem of a broken package, not just the first', () => {
    const report = validatePackage({ value: { schema: 2, id: 'X', name: '', version: 'one two', permissions: ['ui:pages', 'ui:nope'], extra: 1 }, problems: [], scriptName: 'the script' })
    assert.equal(report.ok, false)
    assert.equal(report.package, null)
    for (const fragment of ['Unknown field "extra"', '"schema" must be 1', '"id" must be', '"name" is required', '"version" must be', 'Unknown permission "ui:nope"', '"ui:pages" needs a "script"']) {
      assert.ok(report.problems.some((problem) => problem.includes(fragment)), `missing "${fragment}" in:\n${report.problems.join('\n')}`)
    }
  })

  test('a file that is not JSON is one clear problem', () => {
    const file = join(freshDir('broken'), 'broken.json')
    mkdirSync(join(file, '..'), { recursive: true })
    writeFileSync(file, '{ "schema": 1, ')
    const report = validatePackage(loadPackage(file))
    assert.equal(report.ok, false)
    assert.match(report.problems[0], /^The file is not valid JSON/u)
  })

  test('a syntax error in main.js names the file and the line', () => {
    const dir = sourceDir('page', { 'main.js': 'const x = 1;\nconst y = ;\n' })
    const report = validatePackage(loadPackage(dir))
    assert.equal(report.ok, false)
    assert.deepEqual(report.problems, ['main.js line 2: SyntaxError: Unexpected token \';\''])
  })

  test('plugin.json may not carry script, css or $schema', () => {
    const dir = sourceDir('page')
    const json = JSON.parse(readFileSync(join(dir, 'plugin.json'), 'utf8'))
    writeFileSync(join(dir, 'plugin.json'), JSON.stringify({ $schema: '../plugin.schema.json', ...json, script: 'x', css: 'a{}' }))
    const problems = validatePackage(loadPackage(dir)).problems
    assert.equal(problems.length, 3, problems.join('\n'))
    assert.ok(problems.some((problem) => problem.includes('"script"') && problem.includes('main.js')))
    assert.ok(problems.some((problem) => problem.includes('"css"') && problem.includes('style.css')))
    assert.ok(problems.some((problem) => problem.includes('"$schema"') && problem.includes('plugin.schema.json')))
  })

  test('a newer minAppVersion is a note, not a problem', () => {
    const value = { ...checked(join(EXAMPLES, 'quick-switch.json')), minAppVersion: '9999.0.0' }
    const report = validatePackage({ value, problems: [], scriptName: 'the script' }, { appVersion: '1.0.0' })
    assert.equal(report.ok, true)
    assert.match(report.notes.join('\n'), /Needs Luxury Switchboard 9999\.0\.0 or newer/u)
  })

  test('a missing path or a directory without plugin.json is a usage error', () => {
    assert.throws(() => loadPackage(join(scratch, 'nowhere')), UsageError)
    const empty = freshDir('empty')
    mkdirSync(empty)
    assert.throws(() => loadPackage(empty), UsageError)
  })
})

describe('pack', () => {
  test('writes <dir>/dist/<id>-<version>.json: the manifest plus main.js, LF, trailing newline', () => {
    const dir = sourceDir('page', {}, { id: 'acme.packed' })
    const result = packDirectory(dir)
    assert.equal(result.out, join(dir, 'dist', 'acme.packed-0.1.0.json'))
    const text = readFileSync(result.out, 'utf8')
    assert.equal(result.bytes, Buffer.byteLength(text))
    assert.ok(text.endsWith('}\n'))
    assert.ok(!text.includes('\r'))
    const packed = JSON.parse(text)
    assert.equal(`${JSON.stringify(packed, null, 2)}\n`, text)
    const source = JSON.parse(readFileSync(join(dir, 'plugin.json'), 'utf8'))
    assert.deepEqual(packed, { ...source, script: readFileSync(join(dir, 'main.js'), 'utf8') })
    assert.deepEqual(Object.keys(packed), ['schema', 'id', 'name', 'version', 'author', 'description', 'permissions', 'settings', 'script'])
    // The packed file is a package in its own right and packs to itself.
    const again = validatePackage(loadPackage(result.out))
    assert.equal(again.ok, true, again.problems.join('\n'))
    assert.equal(packedText(again), text)
  })

  test('a theme packs style.css as css, to --out', () => {
    const dir = sourceDir('theme', {}, { id: 'acme.packed-theme' })
    const out = join(scratch, 'out', 'theme.json')
    const result = packDirectory(dir, { out })
    assert.equal(result.out, out)
    const packed = JSON.parse(readFileSync(out, 'utf8'))
    assert.equal(packed.css, readFileSync(join(dir, 'style.css'), 'utf8'))
    assert.ok(packed.css.includes(':root[data-theme="plugin:acme.packed-theme"]'))
    assert.equal(packed.script, undefined)
  })

  test('writes nothing when the package has problems', () => {
    const dir = sourceDir('page', { 'main.js': 'const y = ;\n' })
    const result = packDirectory(dir)
    assert.equal(result.out, '')
    assert.equal(result.report.ok, false)
    assert.equal(existsSync(join(dir, 'dist')), false)
  })

  test('takes a source directory, not a file', () => {
    assert.throws(() => packDirectory(LATENCY_BOARD), UsageError)
  })
})

describe('init', () => {
  for (const kind of TEMPLATE_KINDS) {
    test(`the ${kind} template validates and runs`, async () => {
      const dir = sourceDir(kind)
      const report = await runPackage(checked(dir), { seconds: 0 })
      assert.deepEqual(report.problems, [])
      assert.equal(report.ok, true)
      assert.equal(report.state, kind === 'theme' ? 'active' : 'running')
    })
  }

  test('the id and name default to the directory name', () => {
    const dir = join(freshDir('named'), 'My Plugin')
    const result = initDirectory(dir, { kind: 'command' })
    assert.equal(result.id, 'acme.my-plugin')
    assert.equal(result.name, 'My Plugin')
    const json = JSON.parse(readFileSync(join(dir, 'plugin.json'), 'utf8'))
    assert.equal(json.id, 'acme.my-plugin')
    assert.equal(json.name, 'My Plugin')
  })

  test('--id is also written into the theme selector', () => {
    const dir = sourceDir('theme', {}, { id: 'acme.renamed', name: 'Renamed' })
    const css = readFileSync(join(dir, 'style.css'), 'utf8')
    assert.ok(css.includes('plugin:acme.renamed'))
    assert.ok(!css.includes('acme.hello-theme'))
  })

  test('refuses a directory that has files, a bad id and an unknown kind', () => {
    const dir = sourceDir('page')
    assert.throws(() => initDirectory(dir, { kind: 'page' }), UsageError)
    assert.throws(() => initDirectory(freshDir(), { kind: 'page', id: 'Not An Id' }), UsageError)
    assert.throws(() => initDirectory(freshDir(), { kind: 'widget' }), UsageError)
  })
})

describe('run', () => {
  test('renders latency-board against the fixture control plane', async () => {
    const report = await runPackage(checked(LATENCY_BOARD), { seconds: 0, events: ['board:refresh'] })
    assert.deepEqual(report.problems, [])
    assert.equal(report.state, 'running')
    assert.deepEqual(report.pages.map((page) => page.id), ['board'])
    const requests = [...nodes(report.pages[0].tree)].find((node) => node.label === 'Requests')
    assert.equal(requests?.value, 486)
  })

  test('the page template counts clicks into storage and reads its setting', async () => {
    const report = await runPackage(checked(sourceDir('page')), { seconds: 0, events: ['main:add', 'main:add'], settings: ['greeting=Hi there'] })
    assert.deepEqual(report.problems, [])
    const tree = [...nodes(report.pages[0].tree)]
    assert.equal(tree.find((node) => node.type === 'heading')?.text, 'Hi there')
    assert.equal(tree.find((node) => node.type === 'metric')?.value, 2)
    assert.ok(Object.values(report.storage).includes('2'))
  })

  test('the command template registers both commands and runs them', async () => {
    const report = await runPackage(checked(sourceDir('command')), { seconds: 0, commands: ['hello', 'copy-time'] })
    assert.deepEqual(report.problems, [])
    assert.deepEqual(report.commands.map((command) => command.id), ['hello', 'copy-time'])
    assert.equal(report.notifications.length, 2)
    assert.equal(report.clipboard.length, 1)
    assert.match(report.clipboard[0], /^\d{4}-\d\d-\d\dT/u)
  })

  test('the theme template contributes a theme and a stylesheet', async () => {
    const dir = sourceDir('theme')
    const report = await runPackage(checked(dir), { seconds: 0 })
    const theme = JSON.parse(readFileSync(join(dir, 'plugin.json'), 'utf8')).theme
    assert.deepEqual(report.themes, [{ name: theme.name, scheme: theme.scheme, tokens: Object.keys(theme.tokens).length }])
    assert.equal(report.styles, 1)
  })

  test('a top-level throw is a crash, with the stack in main.js lines', async () => {
    const dir = sourceDir('page', { 'main.js': 'const a = 1;\nfunction go() { throw new Error(\'boom\'); }\ngo();\n' })
    const report = await runPackage(checked(dir), { seconds: 0, scriptName: 'main.js' })
    assert.equal(report.ok, false)
    assert.equal(report.state, 'crashed')
    assert.match(report.reason, /Error: boom/u)
    assert.match(report.reason, /at go \(main\.js:2:\d+\)/u)
    assert.doesNotMatch(report.reason, /vm-worker|node:|host-entry/u)
  })

  test('a refused tree fails the run and says why', async () => {
    const dir = sourceDir('page', { 'main.js': "await switchboard.ui.registerPage({ id: 'main', title: 'T' });\nawait switchboard.ui.render('main', { type: 'stack', childern: [] });\n" })
    const report = await runPackage(checked(dir), { seconds: 0 })
    assert.equal(report.ok, false)
    assert.ok(report.problems.some((problem) => problem.includes('childern')), report.problems.join('\n'))
  })

  test('a handler that throws is a fault, and the run fails', async () => {
    const dir = sourceDir('page', { 'main.js': "await switchboard.ui.registerPage({ id: 'main', title: 'T' });\nswitchboard.ui.onEvent('main', (event) => { if (event.action === 'x') throw new Error('handler broke'); });\n" })
    const report = await runPackage(checked(dir), { seconds: 0, events: ['main:x'] })
    assert.equal(report.ok, false)
    assert.equal(report.state, 'running')
    assert.equal(report.faults, 1)
    assert.ok(report.logs.some((entry) => entry.level === 'error' && entry.text.includes('handler broke')))
  })

  test('events and commands the plugin never registered are problems', async () => {
    const report = await runPackage(checked(sourceDir('page')), { seconds: 0, events: ['nope:x'], commands: ['zzz'] })
    assert.equal(report.ok, false)
    assert.ok(report.problems.some((problem) => problem.includes('no page "nope"')))
    assert.ok(report.problems.some((problem) => problem.includes('--command zzz')))
  })

  test('a narrower --grant is honoured and an undeclared one is noted', async () => {
    const report = await runPackage(checked(sourceDir('page')), { seconds: 0, grant: 'ui:pages,ui:css' })
    assert.deepEqual(report.grants, ['ui:pages'])
    assert.ok(report.notes.some((note) => note.includes('ui:css')))
  })

  test('minAppVersion holds a run back until --app-version is new enough', async () => {
    const pkg = { ...checked(join(EXAMPLES, 'quick-switch.json')), minAppVersion: '2.0.0' }
    const old = await runPackage(pkg, { seconds: 0, appVersion: '1.0.0' })
    assert.equal(old.ok, false)
    assert.equal(old.state, 'not started')
    const current = await runPackage(pkg, { seconds: 0, appVersion: '2.0.0' })
    assert.deepEqual(current.problems, [])
  })

  const example = (name) => validatePackage(loadPackage(join(EXAMPLES, name)), { appVersion: '1.0.66' }).package
  const run66 = (name, options = {}) => runPackage(example(name), { seconds: 0, appVersion: '1.0.66', ...options })

  test('usage report reads the fixture control plane and exports its table as CSV', async () => {
    const report = await run66('usage-report', { events: ['main:export'] })
    assert.deepEqual(report.problems, [])
    assert.deepEqual(report.notes, [])
    assert.equal(report.files.length, 1)
    assert.equal(report.files[0].save, 'usage-24h.csv')
  })

  test('daily budget asks before a reset from its settings section, and honors no', async () => {
    const report = await run66('daily-budget', { events: ['settings:budget:reset'], confirm: 'no', settings: ['limit=10'] })
    assert.deepEqual(report.problems, [])
    assert.equal(report.confirms.length, 1)
    assert.equal(report.confirms[0].answer, false)
    assert.match(report.status[0].text, /^Budget \d+\/10$/u)
  })

  test('snippets seed a collection, register palette commands and export them', async () => {
    const report = await run66('snippets', { events: ['main:export'] })
    assert.deepEqual(report.problems, [])
    assert.ok(report.commands.length > 0 && report.commands.every((command) => command.id.startsWith('copy.')))
    assert.equal(report.files.length, 1)
  })

  test('tetris draws on its canvas every frame without refused draws', async () => {
    const report = await run66('tetris')
    assert.deepEqual(report.problems, [])
    assert.equal(report.frames, 5)
    assert.ok(Object.values(report.draws).some((draw) => draw.count > 0 && draw.ops > 0))
  })

  test('net.fetch is never sent: not_allowed unless a --fetch prefix answers it', async () => {
    const unmocked = await run66('status-widget', { settings: ['apiKey=fictional-key'] })
    assert.deepEqual(unmocked.problems, [])
    assert.ok(unmocked.fetches.length > 0 && unmocked.fetches.every((fetch) => fetch.result.startsWith('not_allowed')))
    const mocked = await run66('status-widget', { settings: ['apiKey=fictional-key'], fetch: ['https://status.example.com/={"status":"operational","components":[]}'] })
    assert.ok(mocked.fetches.length > 0 && mocked.fetches.every((fetch) => fetch.result === 200))
  })

  test('net.fetch refuses what Go refuses: bad methods, too many or oversized headers, secrets in the URL', async () => {
    const dir = sourceDir('page', {
      'plugin.json': JSON.stringify({ schema: 1, id: 'me.fetch', name: 'F', version: '1.0.0', minAppVersion: '1.0.66', permissions: ['net:fetch'], network: { hosts: ['api.example.com'] } }),
      'main.js': `const url = 'https://api.example.com/'
for (const init of [{ method: 'HEAD' }, { headers: Object.fromEntries(Array.from({ length: 33 }, (_, i) => ['x-' + i, '1'])) }, { headers: { 'x-big': 'a'.repeat(8193) } }]) {
  await switchboard.net.fetch(url, init).catch(() => {})
}
await switchboard.net.fetch(url + '{{secret:token}}').catch(() => {})`,
    })
    const report = await runPackage(validatePackage(loadPackage(dir), { appVersion: '1.0.66' }).package, { seconds: 0, appVersion: '1.0.66', fetch: ['https://api.example.com/=ok'] })
    assert.deepEqual(report.fetches.map((fetch) => fetch.result), ['invalid_plugin', 'invalid_plugin', 'invalid_plugin', 200])
  })

  test('the redactor rewrites the fictional prompt and tags it', async () => {
    const report = await run66('relay-redactor')
    const rewrite = report.hookDecisions.find((decision) => decision.phase === 'request')
    assert.equal(rewrite?.outcome, 'rewrite')
    assert.deepEqual(rewrite.tags, ['redacted'])
    assert.match(rewrite.body, /\[email\].*\[phone\]/u)
    assert.doesNotMatch(rewrite.body, /@/u)
  })

  test('a layout can never hide Plugins or Settings', async () => {
    const dir = sourceDir('page', {
      'plugin.json': JSON.stringify({ schema: 1, id: 'me.layout', name: 'L', version: '1.0.0', minAppVersion: '1.0.66', permissions: ['ui:layout'] }),
      'main.js': "await switchboard.ui.setLayout({ hidden: ['plugins', 'settings', 'tunnel'] })",
    })
    const report = await runPackage(validatePackage(loadPackage(dir), { appVersion: '1.0.66' }).package, { seconds: 0, appVersion: '1.0.66' })
    assert.deepEqual(report.layout.map((layout) => layout.hidden), [['tunnel']])
  })

  test('usage mistakes in run options are usage errors', async () => {
    const pkg = checked(sourceDir('page'))
    await assert.rejects(runPackage(pkg, { seconds: 0, settings: ['nope=1'] }), UsageError)
    await assert.rejects(runPackage(pkg, { seconds: 0, events: [':x'] }), UsageError)
    await assert.rejects(runPackage(pkg, { seconds: 0, events: ['main:'] }), UsageError)
  })

  test('tidyStack maps plugin.js lines to script lines and drops harness frames', () => {
    const stack = 'Error: x\n    at go (plugin.js:12:5)\n    at plugin.js:3:1\n    at Script.runInContext (node:vm:149:12)\n    at host-entry.js:1:12'
    assert.equal(tidyStack(stack, 10), 'Error: x\n    at go (main.js:2:5)')
    assert.equal(tidyStack('one line', 10), 'one line')
  })
})

describe('command line', () => {
  const page = sourceDir('page')
  const cases = [
    { args: [], code: 2 },
    { args: ['help'], code: 0, stdout: /NOT a sandbox/u },
    { args: ['--help'], code: 0, stdout: /^Usage: pnpm plugin <command>/u },
    { args: ['frobnicate'], code: 2, stderr: /Unknown command "frobnicate"/u },
    { args: ['validate'], code: 2, stderr: /exactly one path/u },
    { args: ['validate', LATENCY_BOARD, LATENCY_BOARD], code: 2 },
    { args: ['validate', LATENCY_BOARD, '--bogus'], code: 2 },
    { args: ['validate', join(scratch, 'missing.json')], code: 2, stderr: /does not exist/u },
    { args: ['validate', LATENCY_BOARD], code: 0, stdout: /^✓ example\.latency-board 1\.0\.0 is valid/u },
    { args: ['validate', page], code: 0 },
    { args: ['init', page], code: 2, stderr: /not empty/u },
    { args: ['init', join(scratch, 'kind'), '--kind', 'widget'], code: 2 },
    { args: ['pack', LATENCY_BOARD], code: 2 },
    { args: ['run', LATENCY_BOARD, '--seconds', 'x'], code: 2 },
    { args: ['run', LATENCY_BOARD, '--confirm', 'maybe'], code: 2 },
    { args: ['run', LATENCY_BOARD, '--app-version', '1.0'], code: 2 },
    { args: ['run', page, '--setting', 'nope=1', '--seconds', '0'], code: 2 },
    { args: ['run', join(EXAMPLES, 'quick-switch.json'), '--seconds', '0'], code: 0, stdout: /^✓ running/u },
  ]
  for (const { args, code, stdout, stderr } of cases) {
    test(`plugin ${args.map((arg) => (arg.startsWith(scratch) || arg.startsWith(ROOT) ? `<${arg.split(/[\\/]/u).at(-1)}>` : arg)).join(' ') || '(nothing)'} exits ${code}`, () => {
      const result = cli(...args)
      assert.equal(result.code, code, `stdout:\n${result.stdout}\nstderr:\n${result.stderr}`)
      if (stdout) assert.match(result.stdout, stdout)
      if (stderr) assert.match(result.stderr, stderr)
    })
  }

  test('validate --json reports problems and sizes, exit 1', () => {
    const file = join(scratch, 'bad.json')
    writeFileSync(file, JSON.stringify({ schema: 1, id: 'acme.bad', name: 'Bad', version: '1', permissions: ['ui:css'], css: '@import url(x);' }))
    const result = cli('validate', file, '--json')
    assert.equal(result.code, 1)
    const report = JSON.parse(result.stdout)
    assert.equal(report.ok, false)
    assert.deepEqual(report.problems, ['"css" may not contain @import or </style>.'])
    assert.equal(report.sizes.cssLimit, 65536)
    assert.equal(report.package, undefined)
  })

  test('pack prints where the package went; a crash exits 1', () => {
    const dir = sourceDir('command', {}, { id: 'acme.cli-pack' })
    const packed = cli('pack', dir)
    assert.equal(packed.code, 0, packed.stderr)
    assert.ok(existsSync(join(dir, 'dist', 'acme.cli-pack-0.1.0.json')))
    writeFileSync(join(dir, 'main.js'), "throw new Error('boom');\n")
    const crashed = cli('run', dir, '--seconds', '0')
    assert.equal(crashed.code, 1)
    assert.match(crashed.stdout, /Crashed: The script failed while starting: Error: boom\n {4}at main\.js:1:7/u)
  })
})

// The schema is hand-written for editors. These tests hold it to the app:
// the constants it repeats, and a table of values both must judge alike.
describe('plugin.schema.json', () => {
  const ANNOTATIONS = new Set(['$schema', '$id', '$defs', 'title', 'description', 'default'])
  const KEYWORDS = new Set(['$ref', 'type', 'const', 'enum', 'pattern', 'minLength', 'maxLength', 'minimum', 'maximum', 'items', 'minItems', 'maxItems', 'uniqueItems', 'properties', 'required', 'additionalProperties', 'propertyNames', 'maxProperties', 'oneOf', 'not', 'contains', 'if', 'then', 'else'])
  const SCHEMA_VALUED = new Set(['items', 'additionalProperties', 'propertyNames', 'not', 'contains', 'if', 'then', 'else'])

  const typeOf = (value) => (value === null ? 'null' : Array.isArray(value) ? 'array' : Number.isInteger(value) ? 'integer' : typeof value)
  const same = (a, b) => JSON.stringify(a) === JSON.stringify(b)
  const resolveRef = (ref) => ref.replace(/^#\//u, '').split('/').reduce((node, key) => node[key], SCHEMA)

  /** The draft 2020-12 subset the schema uses; anything else fails the walk below. */
  function errors(schema, value, at = '$') {
    const found = []
    const fail = (message) => found.push(`${at}: ${message}`)
    if (schema.$ref) found.push(...errors(resolveRef(schema.$ref), value, at))
    if ('const' in schema && !same(schema.const, value)) fail(`is not ${JSON.stringify(schema.const)}`)
    if (schema.enum && !schema.enum.some((option) => same(option, value))) fail('is not one of the allowed values')
    if (schema.type) {
      const type = typeOf(value)
      if (type !== schema.type && !(schema.type === 'number' && type === 'integer')) return [...found, `${at}: is not ${schema.type}`]
    }
    if (typeof value === 'string') {
      const length = [...value].length
      if (schema.minLength !== undefined && length < schema.minLength) fail('is too short')
      if (schema.maxLength !== undefined && length > schema.maxLength) fail('is too long')
      if (schema.pattern !== undefined && !new RegExp(schema.pattern, 'u').test(value)) fail(`does not match ${schema.pattern}`)
    }
    if (typeof value === 'number') {
      if (schema.minimum !== undefined && value < schema.minimum) fail('is too small')
      if (schema.maximum !== undefined && value > schema.maximum) fail('is too large')
    }
    if (Array.isArray(value)) {
      if (schema.minItems !== undefined && value.length < schema.minItems) fail('has too few items')
      if (schema.maxItems !== undefined && value.length > schema.maxItems) fail('has too many items')
      if (schema.uniqueItems && new Set(value.map((item) => JSON.stringify(item))).size !== value.length) fail('repeats an item')
      if (schema.items) value.forEach((item, index) => found.push(...errors(schema.items, item, `${at}[${index}]`)))
      if (schema.contains && !value.some((item) => errors(schema.contains, item, at).length === 0)) fail('contains no matching item')
    }
    if (typeOf(value) === 'object') {
      const keys = Object.keys(value)
      for (const key of schema.required ?? []) if (!keys.includes(key)) fail(`needs "${key}"`)
      if (schema.maxProperties !== undefined && keys.length > schema.maxProperties) fail('has too many properties')
      for (const key of keys) {
        if (schema.propertyNames) found.push(...errors(schema.propertyNames, key, `${at}{${key}}`))
        if (schema.properties && key in schema.properties) found.push(...errors(schema.properties[key], value[key], `${at}.${key}`))
        else if (schema.additionalProperties === false) fail(`has unknown "${key}"`)
        else if (schema.additionalProperties) found.push(...errors(schema.additionalProperties, value[key], `${at}.${key}`))
      }
    }
    if (schema.oneOf && schema.oneOf.filter((branch) => errors(branch, value, at).length === 0).length !== 1) fail('matches no single variant')
    if (schema.not && errors(schema.not, value, at).length === 0) fail('matches a refused form')
    if (schema.if) {
      const branch = errors(schema.if, value, at).length === 0 ? schema.then : schema.else
      if (branch) found.push(...errors(branch, value, at))
    }
    return found
  }

  const valid = (value) => errors(SCHEMA, value).length === 0

  test('uses only the keywords this check understands', () => {
    const walk = (schema, at) => {
      for (const [key, value] of Object.entries(schema)) {
        assert.ok(KEYWORDS.has(key) || ANNOTATIONS.has(key), `${at}: keyword "${key}" is not checked here`)
        if (SCHEMA_VALUED.has(key) && typeof value === 'object') walk(value, `${at}.${key}`)
        if (key === 'properties' || key === '$defs') for (const [name, child] of Object.entries(value)) walk(child, `${at}.${key}.${name}`)
        if (key === 'oneOf') value.forEach((child, index) => walk(child, `${at}.oneOf[${index}]`))
        if (key === '$ref') assert.ok(resolveRef(value), `${at}: ${value} resolves`)
      }
    }
    walk(SCHEMA, '$')
    assert.equal(SCHEMA.$schema, 'https://json-schema.org/draft/2020-12/schema')
  })

  test('repeats the app constants exactly', () => {
    const top = SCHEMA.properties
    const defs = SCHEMA.$defs
    assert.equal(top.schema.const, manifest.PACKAGE_SCHEMA)
    assert.equal(top.id.pattern, manifest.PLUGIN_ID_PATTERN.source)
    assert.equal(top.name.maxLength, manifest.NAME_LIMIT)
    assert.equal(top.author.maxLength, manifest.AUTHOR_LIMIT)
    assert.equal(top.description.maxLength, manifest.DESCRIPTION_LIMIT)
    assert.equal(top.homepage.maxLength, manifest.HOMEPAGE_BYTES)
    assert.equal(top.css.maxLength, manifest.CSS_BYTES)
    assert.equal(top.script.maxLength, manifest.SCRIPT_BYTES)
    assert.equal(top.theme.properties.name.maxLength, manifest.THEME_NAME_LIMIT)
    assert.deepEqual(defs.permission.enum, [...permissions.PLUGIN_PERMISSIONS])
    assert.equal(top.theme.properties.tokens.maxProperties, tokens.TOKEN_LIMIT)
    assert.equal(top.theme.properties.tokens.propertyNames.pattern, tokens.TOKEN_NAME_PATTERN.source)
    assert.equal(defs.tokenValue.maxLength, tokens.TOKEN_VALUE_BYTES)
    assert.equal(top.settings.maxItems, settings.SETTING_LIMIT)
    assert.equal(defs.settingKey.pattern, settings.SETTING_KEY_PATTERN.source)
    assert.equal(defs.label.maxLength, settings.SETTING_LABEL_LIMIT)
    assert.equal(defs.settingDescription.maxLength, settings.SETTING_DESCRIPTION_LIMIT)
    assert.equal(defs.placeholder.maxLength, settings.SETTING_PLACEHOLDER_LIMIT)
    assert.equal(defs.textSetting.properties.maxLength.maximum, settings.SETTING_TEXT_LIMIT)
    assert.equal(defs.textSetting.properties.default.maxLength, settings.SETTING_TEXT_LIMIT)
    assert.equal(defs.selectSetting.properties.options.maxItems, settings.SETTING_OPTION_LIMIT)
    assert.deepEqual(defs.setting.properties.type.enum, [...settings.SETTING_TYPES])
    assert.deepEqual(defs.setting.oneOf.map((branch) => resolveRef(branch.$ref).properties.type.const), [...settings.SETTING_TYPES])
    assert.equal(top.permissions.maxItems, manifest.PERMISSION_LIMIT)
    assert.equal(top.network.properties.hosts.maxItems, manifest.NETWORK_HOST_LIMIT)
  })

  test('the examples, the templates and their packs fit the schema', () => {
    for (const file of readdirSync(EXAMPLES).filter((name) => name.endsWith('.json'))) {
      assert.deepEqual(errors(SCHEMA, JSON.parse(readFileSync(join(EXAMPLES, file), 'utf8'))), [], file)
    }
    for (const dir of SOURCE_EXAMPLES) assert.deepEqual(errors(SCHEMA, loadPackage(join(EXAMPLES, dir)).value), [], dir)
    for (const kind of TEMPLATE_KINDS) {
      const { out } = packDirectory(sourceDir(kind))
      assert.deepEqual(errors(SCHEMA, JSON.parse(readFileSync(out, 'utf8'))), [], kind)
    }
  })

  // Each row is a package; the schema and the app's checkPackage must agree on
  // it. Rules the schema cannot express (byte sizes of non-ASCII text, which
  // permissions need a script, duplicate setting keys, defaults that depend on
  // other fields) are deliberately absent.
  const base = { schema: 1, id: 'acme.sample', name: 'Sample', version: '1.0.0', permissions: [] }
  const scripted = { ...base, script: '/* reads settings */' }
  const themed = (theme) => ({ ...base, permissions: ['ui:theme'], theme: { name: 'T', scheme: 'dark', tokens: { '--accent': '#fff' }, ...theme } })
  const tokenSet = (count) => Object.fromEntries(Array.from({ length: count }, (_, index) => [`--t${index}`, '#000']))
  const withSettings = (...list) => ({ ...scripted, settings: list })
  const text = (extra = {}) => ({ key: 'greeting', label: 'Greeting', type: 'text', default: 'hi', ...extra })
  const repeat = (char, count) => char.repeat(count)
  const rows = [
    ['the minimal package', base],
    ['schema 2', { ...base, schema: 2 }],
    ['no permissions', { schema: 1, id: 'acme.sample', name: 'Sample', version: '1.0.0' }],
    ['an unknown field', { ...base, extra: true }],
    ['a $schema key', { $schema: './plugin.schema.json', ...base }],
    ...['ab', 'abc', 'a.b-c', 'Acme.x', '-abc', 'abc-', 'a_b', repeat('a', 64), repeat('a', 65)].map((id) => [`id ${JSON.stringify(id)}`, { ...base, id }]),
    ...['', ' ', '\t', 'A', 'a\u0001b', 'a\u0085b', 'Ünïcødé', repeat('n', 64), repeat('n', 65)].map((name) => [`name ${JSON.stringify(name)}`, { ...base, name }]),
    ...['1', '1.2.0-beta.1+sha', 'v 1', '1/2', '', repeat('9', 32), repeat('9', 33)].map((version) => [`version ${JSON.stringify(version)}`, { ...base, version }]),
    ...['', 'Ann', 'a\nb', repeat('a', 64), repeat('a', 65)].map((author) => [`author ${JSON.stringify(author)}`, { ...base, author }]),
    ...['', 'Two\nlines', 'tab\there', repeat('d', 280), repeat('d', 281)].map((description) => [`description ${JSON.stringify(description).slice(0, 24)}`, { ...base, description }]),
    ...['', 'https://example.com', 'https://example.com/a/b?q=1#top', 'https://example.com/a@b', 'http://example.com', 'https://user@example.com', 'https://@example.com', 'https://exa mple.com', 'https://', `https://example.com/${repeat('a', 180)}`, `https://example.com/${repeat('a', 181)}`].map((homepage) => [`homepage ${JSON.stringify(homepage).slice(0, 40)}`, { ...base, homepage }]),
    ...['1.0.65', '0.0.0', '1.0', '1.0.65-beta', '12345.0.0', 'x.y.z'].map((minAppVersion) => [`minAppVersion ${minAppVersion}`, { ...base, minAppVersion }]),
    ['permissions not a list', { ...base, permissions: 'ui:theme' }],
    ['an unknown permission', { ...base, permissions: ['ui:nope'] }],
    ['a repeated permission', { ...base, permissions: ['ui:theme', 'ui:theme'] }],
    ['a theme', themed({})],
    ['a light theme', themed({ scheme: 'light' })],
    ['a theme scheme "dim"', themed({ scheme: 'dim' })],
    ['a theme without tokens', { ...base, permissions: ['ui:theme'], theme: { name: 'T', scheme: 'dark' } }],
    ['a theme with an extra field', themed({ extra: 1 })],
    ['a theme name of 48', themed({ name: repeat('t', 48) })],
    ['a theme name of 49', themed({ name: repeat('t', 49) })],
    ['a blank theme name', themed({ name: '  ' })],
    ['96 tokens', themed({ tokens: tokenSet(96) })],
    ['97 tokens', themed({ tokens: tokenSet(97) })],
    ...['--accent', '--a1-b2', `--a${repeat('b', 47)}`, `--a${repeat('b', 48)}`, '--Accent', '-accent', '--1a', 'accent'].map((name) => [`token name ${name.slice(0, 12)}`, themed({ tokens: { [name]: '#fff' } })]),
    ...['#fff', 'rgb(1 2 3 / 50%)', 'var(--x)', '', 'red; color: blue', 'url(x)', 'URL(x)', 'Expression(1)', 'javascript:1', '@import', 'a{b}', 'a\\b', 'a`b', 'a\u0007b', 'a\u0085b', repeat('a', 120), repeat('a', 121)].map((value) => [`token value ${JSON.stringify(value).slice(0, 20)}`, themed({ tokens: { '--accent': value } })]),
    ['a stylesheet', { ...base, permissions: ['ui:css'], css: 'body { color: red }' }],
    ['a stylesheet with @IMPORT', { ...base, permissions: ['ui:css'], css: '@IMPORT "x";' }],
    ['a stylesheet with </Style', { ...base, permissions: ['ui:css'], css: '</Style><script>' }],
    ['css not a string', { ...base, permissions: ['ui:css'], css: 1 }],
    ['script not a string', { ...base, script: 1 }],
    ['settings not a list', { ...scripted, settings: {} }],
    ['an empty settings list', withSettings()],
    ['a text setting', withSettings(text({ description: 'Shown on top.', placeholder: 'Say hi', maxLength: 80 }))],
    ['a number setting', withSettings({ key: 'limit', label: 'Limit', type: 'number', default: 5, min: 0, max: 10, placeholder: '5' })],
    ['a fractional number setting', withSettings({ key: 'ratio', label: 'Ratio', type: 'number', default: 0.5 })],
    ['a toggle setting', withSettings({ key: 'on', label: 'On', type: 'toggle', default: false })],
    ['a select setting', withSettings({ key: 'mode', label: 'Mode', type: 'select', default: 'a', options: [{ value: 'a', label: 'A' }, { value: 'b', label: 'B' }] })],
    ['24 settings', withSettings(...Array.from({ length: 24 }, (_, index) => text({ key: `s${index}` })))],
    ['25 settings', withSettings(...Array.from({ length: 25 }, (_, index) => text({ key: `s${index}` })))],
    ...['Greeting', '1st', '_x', `a${repeat('b', 31)}`, `a${repeat('b', 32)}`, 'snake_Case9'].map((key) => [`setting key ${key.slice(0, 12)}`, withSettings(text({ key }))]),
    ['a setting that is not an object', withSettings('greeting')],
    ['a setting with an unknown field', withSettings(text({ extra: 1 }))],
    ['a setting with an unknown type', withSettings(text({ type: 'color' }))],
    ['a setting without a label', withSettings(text({ label: undefined }))],
    ['a setting with a blank label', withSettings(text({ label: ' ' }))],
    ['a setting label of 65', withSettings(text({ label: repeat('l', 65) }))],
    ['a setting description with a line break', withSettings(text({ description: 'a\nb' }))],
    ['a setting description of 201', withSettings(text({ description: repeat('d', 201) }))],
    ['a placeholder of 65', withSettings(text({ placeholder: repeat('p', 65) }))],
    ['a setting without a default', withSettings(text({ default: undefined }))],
    ['a text default with a control character', withSettings(text({ default: 'a\tb' }))],
    ['a text default of 257', withSettings(text({ default: repeat('x', 257) }))],
    ['maxLength 0', withSettings(text({ maxLength: 0 }))],
    ['maxLength 256', withSettings(text({ maxLength: 256 }))],
    ['maxLength 257', withSettings(text({ maxLength: 257 }))],
    ['maxLength 1.5', withSettings(text({ maxLength: 1.5 }))],
    ['a text setting with options', withSettings(text({ options: [{ value: 'a', label: 'A' }] }))],
    ['a text setting with min', withSettings(text({ min: 1 }))],
    ['a toggle with a placeholder', withSettings({ key: 'on', label: 'On', type: 'toggle', default: true, placeholder: 'x' })],
    ['a toggle defaulting to "yes"', withSettings({ key: 'on', label: 'On', type: 'toggle', default: 'yes' })],
    ['a number defaulting to "5"', withSettings({ key: 'limit', label: 'Limit', type: 'number', default: '5' })],
    ['a number with maxLength', withSettings({ key: 'limit', label: 'Limit', type: 'number', default: 5, maxLength: 3 })],
    ['a number with a string min', withSettings({ key: 'limit', label: 'Limit', type: 'number', default: 5, min: '0' })],
    ['a select without options', withSettings({ key: 'mode', label: 'Mode', type: 'select', default: 'a' })],
    ['a select with no options', withSettings({ key: 'mode', label: 'Mode', type: 'select', default: 'a', options: [] })],
    ['a select with 33 options', withSettings({ key: 'mode', label: 'Mode', type: 'select', default: 'o0', options: Array.from({ length: 33 }, (_, index) => ({ value: `o${index}`, label: `O${index}` })) })],
    ['a select option without a label', withSettings({ key: 'mode', label: 'Mode', type: 'select', default: 'a', options: [{ value: 'a' }] })],
    ['a select option with an extra field', withSettings({ key: 'mode', label: 'Mode', type: 'select', default: 'a', options: [{ value: 'a', label: 'A', icon: 'x' }] })],
    ['a select with maxLength', withSettings({ key: 'mode', label: 'Mode', type: 'select', default: 'a', options: [{ value: 'a', label: 'A' }], maxLength: 3 })],
    ['a secret setting', withSettings({ key: 'token', label: 'Token', type: 'secret', description: 'Write-only.' })],
    ['a secret with a default', withSettings({ key: 'token', label: 'Token', type: 'secret', default: 'x' })],
    ['a secret with a placeholder', withSettings({ key: 'token', label: 'Token', type: 'secret', placeholder: 'x' })],
    ['8 secrets', withSettings(...Array.from({ length: 8 }, (_, index) => ({ key: `t${index}`, label: 'T', type: 'secret' })))],
    ['every 1.0.66 permission', { ...scripted, permissions: [...permissions.PLUGIN_PERMISSIONS], network: { hosts: ['api.example.com'] } }],
    ...['api.example.com', '*.example.com', 'a-b.example.net', 'example', 'localhost', 'printer.local', '*.local', 'x.internal', '10.0.0.1', '1.2.3.example', 'api.example.com:443', 'https://example.com', 'Example.com', 'a..b', '-a.example.com', '*.*.example.com', `${repeat('a', 63)}.com`, `${repeat('a', 64)}.com`].map((host) => [`network host ${host.length > 40 ? `of ${host.length} bytes` : JSON.stringify(host)}`, { ...scripted, permissions: ['net:fetch'], network: { hosts: [host] } }]),
    ['net:fetch without network', { ...scripted, permissions: ['net:fetch'] }],
    ['network without net:fetch', { ...scripted, permissions: ['storage'], network: { hosts: ['api.example.com'] } }],
    ['no network hosts', { ...scripted, permissions: ['net:fetch'], network: { hosts: [] } }],
    ['17 network hosts', { ...scripted, permissions: ['net:fetch'], network: { hosts: Array.from({ length: 17 }, (_, index) => `h${index}.example.com`) } }],
    ['a repeated network host', { ...scripted, permissions: ['net:fetch'], network: { hosts: ['api.example.com', 'api.example.com'] } }],
    ['network with an extra field', { ...scripted, permissions: ['net:fetch'], network: { hosts: ['api.example.com'], ports: [443] } }],
  ]
  for (const [name, value] of rows) {
    test(`agrees with the app on ${name}`, () => {
      const clean = JSON.parse(JSON.stringify(value)) // drops the undefined fields
      const app = manifest.checkPackage(clean)
      assert.equal(valid(clean), app.ok, app.ok ? `the app accepts it; the schema says ${errors(SCHEMA, clean).join('; ')}` : `the app refuses it (${app.problems.join(' ')}); the schema accepts it`)
    })
  }
})
