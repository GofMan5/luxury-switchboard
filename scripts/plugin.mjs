#!/usr/bin/env node
// pnpm plugin <command>: check, pack, scaffold and try Switchboard plugins with
// the app's own rules (loaded from frontend/src, never re-implemented here).
// Exit codes: 0 ok · 1 the plugin has problems · 2 the command was misused.
import process from 'node:process'
import { relative } from 'node:path'
import { parseArgs } from 'node:util'

const HELP = `Usage: pnpm plugin <command> [options]

Commands
  validate <package.json|dir> [--json]
      Every check the app applies on install, a compile check of the script
      and the byte budgets. A dir is a plugin source directory: plugin.json
      (the package without "script" and "css"), main.js, style.css.
  pack <dir> [-o, --out <file>]
      Validates the source directory and writes the installable package
      (default <dir>/dist/<id>-<version>.json). Nothing is written on failure.
  init <dir> [--kind page|command|theme] [--id acme.hello] [--name "Hello"]
      Scaffolds a source directory that passes validate. Refuses a directory
      that already has files.
  run <package.json|dir> [options]
      Starts the plugin in the app's own PluginHost against the dev fixture
      control plane, then reports its pages (with their trees), commands,
      status, notifications, log and faults.
      NOT a sandbox: the script runs inside this Node process. Trusted code only.
        --seconds <n>              how long to let it run (default 3)
        --grant all|<perm,perm>    what to grant (default: every declared permission)
        --event <pageId>:<action>  show the page, then press the action (repeatable)
        --command <id>             run one of its palette commands (repeatable)
        --setting <key>=<value>    a setting value; JSON or plain text (repeatable)
        --confirm yes|no           the answer to ui.confirm (default yes)
        --app-version <x.y.z>      the app version it sees (default this checkout's)
        --json                     print the report as JSON
  help
      This text.

Exit codes: 0 ok · 1 the plugin has problems (or crashed) · 2 usage error.
JSON Schema for packages and plugin.json: docs/plugins/plugin.schema.json.
`

const COMMANDS = {
  validate: { json: { type: 'boolean' } },
  pack: { out: { type: 'string', short: 'o' } },
  init: { kind: { type: 'string' }, id: { type: 'string' }, name: { type: 'string' } },
  run: {
    seconds: { type: 'string' }, grant: { type: 'string' }, event: { type: 'string', multiple: true },
    command: { type: 'string', multiple: true }, setting: { type: 'string', multiple: true },
    confirm: { type: 'string' }, 'app-version': { type: 'string' }, json: { type: 'boolean' },
  },
}

const out = (text) => process.stdout.write(`${text}\n`)
const show = (path) => relative(process.cwd(), path) || '.'
const kib = (bytes) => `${(bytes / 1024).toFixed(1)} KiB`

class Usage extends Error {}

function parse(command, args) {
  try {
    const { values, positionals } = parseArgs({ args, options: COMMANDS[command], allowPositionals: true, strict: true })
    if (positionals.length !== 1) throw new Usage(`${command} takes exactly one path; got ${positionals.length}.`)
    return { values, path: positionals[0] }
  } catch (error) {
    throw error instanceof Usage ? error : new Usage(error.message)
  }
}

function printReport(report) {
  for (const problem of report.problems) out(`✗ ${problem}`)
  for (const note of report.notes) out(`note: ${note}`)
  const { sizes } = report
  out(`  script   ${kib(sizes.scriptBytes)} / ${kib(sizes.scriptLimit)}`)
  out(`  css      ${kib(sizes.cssBytes)} / ${kib(sizes.cssLimit)}`)
  out(`  tokens   ${sizes.tokens} / ${sizes.tokenLimit}`)
  out(`  settings ${sizes.settings} / ${sizes.settingLimit}`)
  out(`  package  ${kib(sizes.packageBytes)}`)
}

function jsonReport(report) {
  const { package: _pkg, value: _value, ...rest } = report
  return rest
}

async function validate(args) {
  const { values, path } = parse('validate', args)
  const { loadPackage, validatePackage } = await import('./plugin/package.mjs')
  const report = validatePackage(loadPackage(path))
  if (values.json) out(JSON.stringify(jsonReport(report), null, 2))
  else {
    out(`${report.ok ? '✓' : '✗'} ${report.id || show(path)}${report.version ? ` ${report.version}` : ''}${report.ok ? ' is valid' : `: ${report.problems.length} problem${report.problems.length === 1 ? '' : 's'}`}`)
    printReport(report)
  }
  return report.ok ? 0 : 1
}

async function pack(args) {
  const { values, path } = parse('pack', args)
  const { packDirectory } = await import('./plugin/package.mjs')
  const result = packDirectory(path, { out: values.out })
  if (!result.out) {
    out(`✗ Not packed: ${result.report.problems.length} problem${result.report.problems.length === 1 ? '' : 's'}`)
    printReport(result.report)
    return 1
  }
  for (const note of result.report.notes) out(`note: ${note}`)
  out(`✓ ${show(result.out)} (${kib(result.bytes)})`)
  return 0
}

async function init(args) {
  const { values, path } = parse('init', args)
  const { initDirectory } = await import('./plugin/package.mjs')
  const result = initDirectory(path, values)
  out(`✓ ${result.kind} plugin ${result.id} in ${show(result.dir)}: ${result.files.join(', ')}`)
  out(`  next: pnpm plugin validate ${show(result.dir)} · pnpm plugin run ${show(result.dir)} · pnpm plugin pack ${show(result.dir)}`)
  return 0
}

function printRun(report) {
  out(`${report.ok ? '✓' : '✗'} ${report.state} · grants ${report.grants?.join(', ') || 'none'}`)
  for (const problem of report.problems) out(`✗ ${problem}`)
  for (const note of report.notes) out(`note: ${note}`)
  for (const page of report.pages ?? []) {
    out(`page ${page.id} "${page.title}"${page.icon ? ` icon=${page.icon}` : ''}${page.badge ? ` badge=${JSON.stringify(page.badge)}` : ''}${page.problem ? ` ✗ ${page.problem}` : ''}`)
    if (page.tree) out(JSON.stringify(page.tree, null, 2).replace(/^/gmu, '    '))
  }
  for (const command of report.commands ?? []) out(`command ${command.id} "${command.title}" [${command.group}]`)
  for (const status of report.status ?? []) out(`status ${status.id}: ${status.text}`)
  for (const theme of report.themes ?? []) out(`theme "${theme.name}" ${theme.scheme}, ${theme.tokens} tokens`)
  if (report.styles) out(`stylesheet applied`)
  for (const notice of report.notifications ?? []) out(`notify [${notice.severity}] ${JSON.stringify(notice.title)}${notice.body ? ` ${JSON.stringify(notice.body)}` : ''}`)
  for (const ask of report.confirms ?? []) out(`confirm "${ask.title}" → ${ask.answer ? 'yes' : 'no'}`)
  for (const text of report.clipboard ?? []) out(`clipboard ${JSON.stringify(text)}`)
  for (const route of report.navigations ?? []) out(`navigate ${route}`)
  for (const entry of report.logs ?? []) out(`log ${entry.level.padEnd(5)} ${entry.text}`)
}

async function run(args) {
  const { values, path } = parse('run', args)
  const seconds = values.seconds === undefined ? 3 : Number(values.seconds)
  if (!Number.isFinite(seconds) || seconds < 0 || seconds > 600) throw new Usage('--seconds takes a number from 0 to 600.')
  if (values.confirm !== undefined && values.confirm !== 'yes' && values.confirm !== 'no') throw new Usage('--confirm takes yes or no.')
  const { loadPackage, validatePackage } = await import('./plugin/package.mjs')
  const { runPackage } = await import('./plugin/run.mjs')
  const appVersion = values['app-version']
  if (appVersion !== undefined && !/^\d{1,4}\.\d{1,4}\.\d{1,4}$/u.test(appVersion)) throw new Usage('--app-version takes x.y.z.')
  const loaded = loadPackage(path)
  const checked = validatePackage(loaded, appVersion ? { appVersion } : {})
  if (!checked.ok) {
    if (values.json) out(JSON.stringify({ ok: false, state: 'not started', problems: checked.problems, notes: checked.notes }, null, 2))
    else { out('✗ Not started: the package does not validate.'); printReport(checked) }
    return 1
  }
  const report = await runPackage(checked.package, {
    seconds, grant: values.grant, events: values.event, commands: values.command, settings: values.setting,
    confirm: values.confirm, appVersion, scriptName: loaded.source === 'directory' ? 'main.js' : 'script',
  })
  if (values.json) out(JSON.stringify(report, null, 2))
  else printRun(report)
  return report.ok ? 0 : 1
}

async function main(argv) {
  const [command, ...args] = argv
  if (command === undefined || command === 'help' || command === '--help' || command === '-h') {
    process.stdout.write(HELP)
    return command === undefined ? 2 : 0
  }
  if (args.includes('--help') || args.includes('-h')) {
    process.stdout.write(HELP)
    return 0
  }
  const handlers = { validate, pack, init, run }
  if (!Object.hasOwn(handlers, command)) throw new Usage(`Unknown command "${command}". Commands: validate, pack, init, run, help.`)
  return handlers[command](args)
}

try {
  // Loading the domain first: on an old Node it explains and exits 2.
  await import('./plugin/domain.mjs')
  const { UsageError } = await import('./plugin/package.mjs')
  try {
    process.exitCode = await main(process.argv.slice(2))
  } catch (error) {
    if (error instanceof Usage || error instanceof UsageError) {
      process.stderr.write(`${error.message}\nRun "pnpm plugin help" for usage.\n`)
      process.exitCode = 2
    } else throw error
  }
} catch (error) {
  process.stderr.write(`${error instanceof Error ? error.stack : String(error)}\n`)
  process.exitCode = 1
}
