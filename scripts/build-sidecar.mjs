import { execFileSync } from 'node:child_process'
import { mkdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const workspace = dirname(dirname(fileURLToPath(import.meta.url)))
const rustc = execFileSync('rustc', ['-vV'], { encoding: 'utf8' })
const target = rustc.match(/^host:\s+(\S+)$/mu)?.[1]
if (!target) throw new Error('Unable to determine the Rust host target')

// The public edition is compiled without the publishing stack, so the binary has
// no tunnel, client governance or SSH publisher to reach at all.
const edition = process.env.SWITCHBOARD_EDITION === 'public' ? 'public' : 'owner'
const buildArguments = ['build', '-trimpath']
if (edition === 'public') buildArguments.push('-tags', 'public')

const binaryDirectory = join(workspace, 'src-tauri', 'binaries')
const output = join(binaryDirectory, `switchboard-sidecar-${target}${target.includes('-windows-') ? '.exe' : ''}`)
mkdirSync(binaryDirectory, { recursive: true })
execFileSync('go', [...buildArguments, '-o', output, './cmd/switchboard-sidecar'], {
  cwd: join(workspace, 'backend'),
  stdio: 'inherit',
})
console.log(`Built ${edition} sidecar: ${output}`)
