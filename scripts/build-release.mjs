import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { chmodSync, copyFileSync, mkdirSync, readFileSync, readdirSync, statSync, unlinkSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const workspace = dirname(dirname(fileURLToPath(import.meta.url)))
const version = JSON.parse(readFileSync(join(workspace, 'src-tauri', 'tauri.conf.json'), 'utf8')).version
assertVersionsAgree(workspace, version)
const releaseDirectory = join(workspace, 'artifacts', 'release')
const architecture = {
  x64: { windows: 'x64', appImage: 'x86_64', deb: 'amd64' },
  arm64: { windows: 'arm64', appImage: 'aarch64', deb: 'arm64' },
}[process.arch]

if (!process.argv.includes('--checksums-only')) {
  if (!architecture || (process.platform !== 'win32' && process.platform !== 'linux')) {
    throw new Error(`Unsupported release host: ${process.platform}/${process.arch}`)
  }
  const pnpm = process.env.npm_execpath
  if (!pnpm) throw new Error('Run the release build through pnpm')
  execFileSync(process.execPath, [pnpm, 'exec', 'tauri', 'build', '--ci'], { cwd: workspace, stdio: 'inherit' })
  mkdirSync(releaseDirectory, { recursive: true })

  const artifacts = process.platform === 'win32'
    ? [{ bundle: 'nsis', suffix: '.exe', name: `Switchboard-${version}-windows-${architecture.windows}-setup.exe` }]
    : [
        { bundle: 'appimage', suffix: '.AppImage', name: `Switchboard-${version}-linux-${architecture.appImage}.AppImage`, executable: true },
        { bundle: 'deb', suffix: '.deb', name: `Switchboard-${version}-linux-${architecture.deb}.deb` },
      ]

  for (const artifact of artifacts) {
    const sourceDirectory = join(workspace, 'src-tauri', 'target', 'release', 'bundle', artifact.bundle)
    const candidates = readdirSync(sourceDirectory)
      .filter((name) => name.endsWith(artifact.suffix) && name.includes(version))
      .map((name) => join(sourceDirectory, name))
      .filter((path) => statSync(path).isFile())
    if (candidates.length !== 1) {
      throw new Error(`Expected one ${artifact.bundle} artifact for ${version}, found ${candidates.length}`)
    }
    const destination = join(releaseDirectory, artifact.name)
    copyFileSync(candidates[0], destination)
    if (artifact.executable) chmodSync(destination, 0o755)
    console.log(`Collected ${destination}`)
  }
}

mkdirSync(releaseDirectory, { recursive: true })
for (const name of readdirSync(releaseDirectory)) {
  if (/\.(?:AppImage|deb|exe)$/u.test(name) && !name.startsWith(`Switchboard-${version}-`)) {
    unlinkSync(join(releaseDirectory, name))
  }
}
const releaseFiles = readdirSync(releaseDirectory)
  .filter((name) => name.startsWith(`Switchboard-${version}-`) && /\.(?:AppImage|deb|exe)$/u.test(name))
  .sort((left, right) => left.localeCompare(right, 'en'))
if (releaseFiles.length === 0) throw new Error('No release artifacts found')

const checksums = releaseFiles.map((name) => {
  const digest = createHash('sha256').update(readFileSync(join(releaseDirectory, name))).digest('hex').toUpperCase()
  return `${digest}  ${name}`
})
writeFileSync(join(releaseDirectory, 'SHA256SUMS.txt'), `${checksums.join('\n')}\n`, 'utf8')
console.log(`Release folder: ${releaseDirectory}`)

// Every surface that states the product version must agree with the bundle, or the
// interface, the installer and the recorded checksums start describing different
// builds. This fails the release instead of shipping a stale label.
function assertVersionsAgree(root, expected) {
  const sources = [
    { file: 'package.json', read: (text) => JSON.parse(text).version },
    { file: join('frontend', 'package.json'), read: (text) => JSON.parse(text).version },
    { file: join('src-tauri', 'Cargo.toml'), read: (text) => text.match(/^version\s*=\s*"([^"]+)"/mu)?.[1] },
    {
      file: join('backend', 'internal', 'slices', 'system', 'adapters', 'stdio', 'register.go'),
      read: (text) => text.match(/AppVersion\s*=\s*"([^"]+)"/u)?.[1],
    },
  ]
  const mismatched = sources
    .map((source) => ({ file: source.file, found: source.read(readFileSync(join(root, source.file), 'utf8')) }))
    .filter((source) => source.found !== expected)
  if (mismatched.length > 0) {
    const detail = mismatched.map((source) => `${source.file}: ${source.found ?? 'missing'}`).join(', ')
    throw new Error(`Version ${expected} in src-tauri/tauri.conf.json disagrees with ${detail}`)
  }
}
