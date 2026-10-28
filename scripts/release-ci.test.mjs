import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import test from 'node:test'

const workflow = readFileSync(new URL('../.github/workflows/build-desktop.yml', import.meta.url), 'utf8')
const step = workflow.match(/      - name: Check source\r?\n([\s\S]*?)(?=      - name:)/u)?.[1]
assert.ok(step, 'source gate step is absent')
const commands = step.slice(step.indexOf('        run: |') + '        run: |'.length)
  .split(/\r?\n/u).map((line) => line.trim()).filter((line) => line && !line.startsWith('#'))
const shell = step.match(/        shell: (\S+)/u)?.[1]

// Execute the workflow's actual command sequence, substituting only tool bytes:
// the first Go command is a native exit7; all other tools are native success.
// A lost Windows failure would reach sidecar build/cargo and allow installers.
test('source gate stops on the first native failure before building or uploading installers', () => {
  let executable, args
  if (shell === 'bash') {
    executable = process.platform === 'win32'
      ? join(process.env.ProgramFiles ?? 'C:/Program Files', 'Git', 'bin', 'bash.exe') : 'bash'
    if (process.platform === 'win32') assert.ok(existsSync(executable), 'existing Git Bash is required for this smoke check')
    const definitions = `pnpm() { echo "STEP:pnpm $*"; "$SMOKE_NODE" -e 'process.exit(0)'; }\ngo() { echo "STEP:go $*"; "$SMOKE_NODE" -e 'process.exit(7)'; }\ncargo() { echo "STEP:cargo $*"; "$SMOKE_NODE" -e 'process.exit(0)'; }\nnode() { echo "STEP:node $*"; "$SMOKE_NODE" -e 'process.exit(0)'; }\n`
    args = ['--noprofile', '--norc', '-e', '-o', 'pipefail', '-c', definitions + commands.join('\n')]
  } else {
    assert.equal(process.platform, 'win32', 'Windows native-failure regression requires an explicit bash source gate')
    executable = 'powershell.exe'
    const node = process.execPath.replaceAll("'", "''")
    const script = commands.map((command) => `Write-Output 'STEP:${command}'; & '${node}' -e 'process.exit(${command.startsWith('go ') ? 7 : 0})'`).join('\n')
    // GitHub's Windows default wrapper only returns the final LASTEXITCODE.
    args = ['-NoProfile', '-NonInteractive', '-Command', `$ErrorActionPreference='stop'\n${script}\nexit $LASTEXITCODE`]
  }
  const result = spawnSync(executable, args, {
    encoding: 'utf8', timeout: 10_000,
    env: { ...process.env, SMOKE_NODE: process.execPath.replaceAll('\\', '/') },
  })
  assert.ifError(result.error)
  assert.equal(result.status, 7, `native failure was hidden:\n${result.stdout}\n${result.stderr}`)
  assert.match(result.stdout, /STEP:go -C backend test/u)
  assert.doesNotMatch(result.stdout, /STEP:(?:go -C backend vet|pnpm sidecar:build|cargo)/u)
  assert.doesNotMatch(workflow, /(?:name: Build the installers|uses: actions\/upload-artifact@[^\r\n]+)\r?\n\s+if: (?:always\(\)|failure\(\))/u)
})

// Signing rewrites the installer bytes, so every signing step must sit between
// the build and the upload collect checksums from, and must stay off unless a
// release tag of a Windows installer meets an owner-configured SignPath.
test('Windows installers are signed before the checksummed upload, only on configured release tags', () => {
  const build = workflow.slice(workflow.indexOf('\n  build:'), workflow.indexOf('\n  collect:'))
  const steps = build.split(/\r?\n      - /u).slice(1)
  const at = (pattern) => {
    const index = steps.findIndex((body) => pattern.test(body))
    assert.notEqual(index, -1, `step ${pattern} is absent`)
    return index
  }
  const built = at(/^name: Build the installers/u)
  const unsigned = at(/^name: Upload the unsigned installer for SignPath/u)
  const signed = at(/^name: Sign the installer with SignPath/u)
  const verified = at(/^name: Verify and install the signed installer/u)
  const uploaded = at(/^uses: actions\/upload-artifact@\S+\r?\n\s+if: [^\r\n]+\r?\n\s+with:\r?\n\s+name: \$\{\{ matrix\.artifact \}\}\r?$/mu)
  assert.ok(built < unsigned && unsigned < signed && signed < verified && verified < uploaded, 'signing is out of order')

  assert.match(build, /SIGN_WITH_SIGNPATH: \$\{\{ startsWith\(matrix\.artifact, 'switchboard-windows-'\) && github\.ref_type == 'tag' && vars\.SIGNPATH_ORGANIZATION_ID != '' \}\}/u)
  for (const index of [unsigned, signed, verified]) assert.match(steps[index], /if: env\.SIGN_WITH_SIGNPATH == 'true'/u)
  assert.match(steps[signed], /uses: signpath\/github-action-submit-signing-request@[0-9a-f]{40} /u, 'third-party action must be pinned by commit')
  assert.match(steps[signed], /api-token: \$\{\{ secrets\.SIGNPATH_API_TOKEN \}\}/u)
  assert.match(steps[signed], /github-artifact-id: \$\{\{ steps\.unsigned\.outputs\.artifact-id \}\}/u)
  assert.match(steps[verified], /Get-AuthenticodeSignature[\s\S]*-ne 'Valid'[\s\S]*throw/u)

  // collect merges every switchboard-* artifact; an unsigned copy matching it
  // could overwrite the signed installer before checksums are taken.
  const pattern = workflow.match(/pattern: (\S+)/u)?.[1]
  const unsignedName = steps[unsigned].match(/name: (\S+)/gu)?.[1]?.slice('name: '.length)
  assert.equal(pattern, 'switchboard-*')
  assert.ok(unsignedName && !unsignedName.startsWith('switchboard-'), `unsigned artifact ${unsignedName} would be collected`)
  assert.ok(workflow.indexOf('--checksums-only') > workflow.indexOf('\n  collect:'), 'checksums must be computed in collect')
})
