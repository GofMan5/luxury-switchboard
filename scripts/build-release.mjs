import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { chmodSync, copyFileSync, existsSync, mkdirSync, readFileSync, readdirSync, statSync, unlinkSync, writeFileSync } from 'node:fs'
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

// One build, one installer: the full product is the only product. The release
// folder keeps the name scripts and docs already name.
const editions = [
  { id: 'owner', label: '', directory: join(workspace, 'artifacts', 'release') },
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
    env: { ...process.env },
  })
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


