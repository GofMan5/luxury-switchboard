import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { chmodSync, copyFileSync, existsSync, mkdirSync, readFileSync, readdirSync, statSync, unlinkSync, writeFileSync } from 'node:fs'
import { basename, dirname, join } from 'node:path'
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
  const artifacts = process.platform === 'win32'
    ? [{ bundle: 'nsis', suffix: '.exe', name: `${artifactPrefix}-${version}-${edition.label}windows-${architecture.windows}-setup.exe` }]
    : [
        { bundle: 'appimage', suffix: '.AppImage', name: `${artifactPrefix}-${version}-${edition.label}linux-${architecture.appImage}.AppImage`, executable: true },
        { bundle: 'deb', suffix: '.deb', name: `${artifactPrefix}-${version}-${edition.label}linux-${architecture.deb}.deb` },
      ]
  // The bundle directory keeps every installer ever built here, including ones from
  // earlier product names that carry this same version. Collecting by name alone
  // would either find two candidates or, worse, find exactly one and ship a months
  // old binary as a fresh release, so only files this build wrote are considered.
  // A clock-ahead host breaks even that filter - a stale same-version artifact
  // written with a future mtime sails through - so this version's leftovers are
  // deleted before the build and the collector has nothing to find but this run.
  for (const artifact of artifacts) {
    removeStaleBundles(artifact)
  }
  const startedAt = Date.now()
  execFileSync(process.execPath, [pnpm, 'exec', 'tauri', 'build', '--ci'], {
    cwd: workspace,
    stdio: 'inherit',
    env: { ...process.env, SWITCHBOARD_EDITION: edition.id },
  })
  if (edition.id === 'public') assertPublicBuildIsStripped(workspace)
  mkdirSync(edition.directory, { recursive: true })

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

// Removes every bundle output whose name the mtime collector could later claim
// (this bundle type's suffix, the current version), so only files the upcoming
// build writes can be found at all. The version is matched between dashes:
// "1.0.3" must not claim "1.0.30" — both current and legacy product names put
// the version there.
function removeStaleBundles(artifact) {
  const directory = join(workspace, 'src-tauri', 'target', 'release', 'bundle', artifact.bundle)
  if (!existsSync(directory)) return
  for (const name of readdirSync(directory)) {
    if (name.endsWith(artifact.suffix) && name.includes(`-${version}-`)) {
      unlinkSync(join(directory, name))
    }
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
  const pages = ownerPageChunks(root)
  const ownerChunks = new RegExp(`^(?:${pages.join('|')})-`, 'u')
  const bundle = join(root, 'frontend', 'dist', 'assets')
  const owned = readdirSync(bundle).filter((name) => ownerChunks.test(name))
  if (owned.length > 0) {
    throw new Error(`Public interface still bundles owner workspaces: ${owned.join(', ')}`)
  }
  const binaries = join(root, 'src-tauri', 'binaries')
  const sidecars = readdirSync(binaries).filter((name) => name.startsWith('switchboard-sidecar-'))
  if (sidecars.length === 0) throw new Error('Public sidecar is missing')
  const markers = ownerCommandMarkers(root)
  for (const name of sidecars) {
    const bytes = readFileSync(join(binaries, name))
    for (const marker of markers) {
      if (bytes.includes(marker)) {
        throw new Error(`Public sidecar ${name} still contains the owner command ${marker}`)
      }
    }
  }
}

// The owner chunk names are read from App.tsx instead of being restated here:
// the lazy pages behind __OWNER_EDITION__ are exactly the modules the public
// bundle must not carry, so a new owner workspace fails the scan the day it is
// added. An empty read throws, because a scan with no names would pass on
// anything.
function ownerPageChunks(root) {
  const source = readFileSync(join(root, 'frontend', 'src', 'App.tsx'), 'utf8')
  const pages = [...source.matchAll(/__OWNER_EDITION__[^;]*?import\('([^']+)'\)/gu)]
    .map((match) => basename(match[1]))
  if (pages.length === 0) throw new Error('App.tsx lists no __OWNER_EDITION__ pages to strip')
  return pages
}

// The sidecar markers are read from the backend instead of being restated here:
// the commands of the stdio slices edition_owner.go registers (compiled only
// into the owner binary) plus the publisher identity name the SSH stack carries,
// so a new owner command fails the public scan without anyone remembering this
// file. An empty read throws for the same reason as the chunk list.
function ownerCommandMarkers(root) {
  const bootstrap = readFileSync(join(root, 'backend', 'internal', 'app', 'bootstrap', 'edition_owner.go'), 'utf8')
  const packages = [...bootstrap.matchAll(/"github\.com\/luxuryprivate\/switchboard\/backend\/(internal\/slices\/[^"]+\/adapters\/stdio)"/gu)]
    .map((match) => join(root, 'backend', ...match[1].split('/')))
  if (packages.length === 0) throw new Error('edition_owner.go registers no stdio slices to strip-check')
  const markers = []
  for (const directory of packages) {
    for (const file of readdirSync(directory).filter((name) => name.endsWith('.go') && !name.endsWith('_test.go'))) {
      const source = readFileSync(join(directory, file), 'utf8')
      markers.push(...[...source.matchAll(/\.Handle\("([^"]+)"/gu)].map((match) => match[1]))
    }
  }
  if (markers.length === 0) throw new Error('The owner stdio slices register no commands to strip-check')
  const sshRuntime = readFileSync(join(root, 'backend', 'internal', 'slices', 'tunnel', 'adapters', 'ssh', 'runtime.go'), 'utf8')
  const identity = sshRuntime.match(/identityName\s*=\s*"([^"]+)"/u)?.[1]
  if (!identity) throw new Error('The publisher identity name moved; the public scan needs it')
  return [...markers, identity]
}
