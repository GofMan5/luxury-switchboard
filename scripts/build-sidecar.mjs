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
// A native Linux go build defaults to CGO, and the net package then links the
// glibc resolver. linuxdeploy rewrites the rpath of exactly such dynamic
// binaries with its bundled patchelf, and on the Ubuntu runners that rewrite
// was measured to leave a binary the loader segfaults on, so the AppImage leg
// fails after the fact. The sidecar stack is pure Go, so CGO buys nothing
// here: disabling it for Linux host targets makes the binary static — the
// shape every local cross-compile check already validated — and leaves
// nothing for patchelf to rewrite. Windows and macOS inputs stay untouched.
const isLinuxHost = target.includes('-linux-')
execFileSync('go', ['build', '-trimpath', '-o', output, './cmd/switchboard-sidecar'], {
  cwd: join(workspace, 'backend'),
  stdio: 'inherit',
  env: { ...process.env, ...(isLinuxHost ? { CGO_ENABLED: '0' } : {}) },
})
console.log(`Built sidecar: ${output}`)
