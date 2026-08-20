import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { chmodSync, copyFileSync, mkdirSync, readFileSync, readdirSync, statSync, unlinkSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { assertVersionsAgree, readVersion } from './version.mjs'

const workspace = dirname(dirname(fileURLToPath(import.meta.url)))
const version = readVersion(workspace)
assertVersionsAgree(workspace, version)
// Artifact names carry the product name, so a rename must land here too or the
// release folder and the checksums would still advertise the old product.
const artifactPrefix = 'Luxury-Switchboard'
const architecture = {
  x64: { windows: 'x64', appImage: 'x86_64', deb: 'amd64' },
  arm64: { windows: 'arm64', appImage: 'aarch64', deb: 'arm64' },
}[process.arch]

// The owner edition publishes gateways; the public one is compiled without that
// stack. Both ship from one command so they can never drift apart.
//
// Only the public edition carries a label in its file name. Both installers share
// one bundle identifier and one install path, so the public build silently replaces
// an owner installation - naming them alike would leave nothing but the folder to
// tell apart the two files once they are downloaded. The owner name stays bare
// because the root SHA256SUMS.txt and package-friend.ps1 name it.
const editions = [
  { id: 'owner', label: '', directory: join(workspace, 'artifacts', 'release') },
  { id: 'public', label: 'public-', directory: join(workspace, 'artifacts', 'release-public') },
]

for (const edition of editions) {
  if (!process.argv.includes('--checksums-only')) buildEdition(edition)
  writeChecksums(edition)
}

function buildEdition(edition) {
  if (!architecture || (process.platform !== 'win32' && process.platform !== 'linux')) {
    throw new Error(`Unsupported release host: ${process.platform}/${process.arch}`)
  }
  const pnpm = process.env.npm_execpath
  if (!pnpm) throw new Error('Run the release build through pnpm')
  // The bundle directory keeps every installer ever built here, including ones from
  // earlier product names that carry this same version. Collecting by name alone
  // would either find two candidates or, worse, find exactly one and ship a months
  // old binary as a fresh release, so only files this build wrote are considered.
  const startedAt = Date.now()
  execFileSync(process.execPath, [pnpm, 'exec', 'tauri', 'build', '--ci'], {
    cwd: workspace,
    stdio: 'inherit',
    env: { ...process.env, SWITCHBOARD_EDITION: edition.id },
  })
  if (edition.id === 'public') assertPublicBuildIsStripped(workspace)
  mkdirSync(edition.directory, { recursive: true })

  const artifacts = process.platform === 'win32'
    ? [{ bundle: 'nsis', suffix: '.exe', name: `${artifactPrefix}-${version}-${edition.label}windows-${architecture.windows}-setup.exe` }]
    : [
        { bundle: 'appimage', suffix: '.AppImage', name: `${artifactPrefix}-${version}-${edition.label}linux-${architecture.appImage}.AppImage`, executable: true },
        { bundle: 'deb', suffix: '.deb', name: `${artifactPrefix}-${version}-${edition.label}linux-${architecture.deb}.deb` },
      ]

  for (const artifact of artifacts) {
    const sourceDirectory = join(workspace, 'src-tauri', 'target', 'release', 'bundle', artifact.bundle)
    const candidates = readdirSync(sourceDirectory)
      .filter((name) => name.endsWith(artifact.suffix) && name.includes(version))
      .map((name) => join(sourceDirectory, name))
      .filter((path) => {
        const entry = statSync(path)
        return entry.isFile() && entry.mtimeMs >= startedAt
      })
    if (candidates.length !== 1) {
      throw new Error(`Expected one freshly built ${artifact.bundle} artifact for ${version}, found ${candidates.length}`)
    }
    const destination = join(edition.directory, artifact.name)
    copyFileSync(candidates[0], destination)
    if (artifact.executable) chmodSync(destination, 0o755)
    console.log(`Collected ${edition.id}: ${destination}`)
  }
}

function writeChecksums(edition) {
  mkdirSync(edition.directory, { recursive: true })
  for (const name of readdirSync(edition.directory)) {
    if (/\.(?:AppImage|deb|exe)$/u.test(name) && !name.startsWith(`${artifactPrefix}-${version}-`)) {
      unlinkSync(join(edition.directory, name))
    }
  }
  const releaseFiles = readdirSync(edition.directory)
    .filter((name) => name.startsWith(`${artifactPrefix}-${version}-`) && /\.(?:AppImage|deb|exe)$/u.test(name))
    .sort((left, right) => left.localeCompare(right, 'en'))
  if (releaseFiles.length === 0) throw new Error(`No ${edition.id} release artifacts found`)

  const checksums = releaseFiles.map((name) => {
    const digest = createHash('sha256').update(readFileSync(join(edition.directory, name))).digest('hex').toUpperCase()
    return `${digest}  ${name}`
  })
  writeFileSync(join(edition.directory, 'SHA256SUMS.txt'), `${checksums.join('\n')}\n`, 'utf8')
  console.log(`Release folder (${edition.id}): ${edition.directory}`)
}

// A public build that still carries the publishing stack would hand the tunnel to
// anyone who runs the sidecar by hand, so the shipped bytes are checked, not the
// intent of the build flags.
function assertPublicBuildIsStripped(root) {
  const bundle = join(root, 'frontend', 'dist', 'assets')
  const owned = readdirSync(bundle).filter((name) => /^(?:TunnelPage|ClientsPage|SharedControlPage)-/u.test(name))
  if (owned.length > 0) {
    throw new Error(`Public interface still bundles owner workspaces: ${owned.join(', ')}`)
  }
  const binaries = join(root, 'src-tauri', 'binaries')
  const sidecars = readdirSync(binaries).filter((name) => name.startsWith('switchboard-sidecar-'))
  if (sidecars.length === 0) throw new Error('Public sidecar is missing')
  for (const name of sidecars) {
    const bytes = readFileSync(join(binaries, name))
    for (const marker of ['tunnel.configure', 'clients.profile', 'shared.control', 'model-tunnel_ed25519']) {
      if (bytes.includes(marker)) {
        throw new Error(`Public sidecar ${name} still contains the owner command ${marker}`)
      }
    }
  }
}
