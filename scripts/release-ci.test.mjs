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
