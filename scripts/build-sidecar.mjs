import { execFileSync } from 'node:child_process'
import { mkdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const workspace = dirname(dirname(fileURLToPath(import.meta.url)))
const rustc = execFileSync('rustc', ['-vV'], { encoding: 'utf8' })
const target = rustc.match(/^host:\s+(\S+)$/mu)?.[1]
if (!target) throw new Error('Unable to determine the Rust host target')

const binaryDirectory = join(workspace, 'src-tauri', 'binaries')
const output = join(binaryDirectory, `switchboard-sidecar-${target}${target.includes('-windows-') ? '.exe' : ''}`)
mkdirSync(binaryDirectory, { recursive: true })
execFileSync('go', ['build', '-trimpath', '-o', output, './cmd/switchboard-sidecar'], {
  cwd: join(workspace, 'backend'),
  stdio: 'inherit',
})
console.log(`Built sidecar: ${output}`)
