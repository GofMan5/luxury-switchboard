// Single source of truth for the release version. Every surface that states the
// version is listed here once, so the CLI that writes it and the release gate
// that verifies it can never disagree about which files matter.
//
// Releases are patch-only: the line stays 1.0.x forever (1.0.7, 1.0.8, ...,
// 1.0.105). A minor or major bump promises a break this product does not make.
import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

const LOCK_PACKAGE = /(\[\[package\]\]\r?\nname = "switchboard-desktop"\r?\nversion = )"([^"]+)"/u

// The first entry is the source the release reads; the rest must follow it.
export function versionTargets() {
  return [
    {
      file: join('src-tauri', 'tauri.conf.json'),
      read: (text) => JSON.parse(text).version,
      write: replaceJSONVersion,
    },
    { file: 'package.json', read: (text) => JSON.parse(text).version, write: replaceJSONVersion },
    { file: join('frontend', 'package.json'), read: (text) => JSON.parse(text).version, write: replaceJSONVersion },
    {
      file: join('src-tauri', 'Cargo.toml'),
      read: (text) => text.match(/^version\s*=\s*"([^"]+)"/mu)?.[1],
      write: (text, version) => text.replace(/^version\s*=\s*"[^"]+"/mu, `version = "${version}"`),
    },
    // The lockfile states the crate version too. Leaving it behind produces a tree
    // that this gate calls consistent and `cargo --locked` rejects with exit 101,
    // so it is written here instead of by whoever next runs an unlocked build. The
    // match is anchored on the package stanza: the file has thousands of other
    // version lines.
    {
      file: join('src-tauri', 'Cargo.lock'),
      read: (text) => text.match(LOCK_PACKAGE)?.[2],
      write: (text, version) => text.replace(LOCK_PACKAGE, `$1"${version}"`),
    },
    {
      file: join('backend', 'internal', 'slices', 'system', 'adapters', 'stdio', 'register.go'),
      read: (text) => text.match(/AppVersion\s*=\s*"([^"]+)"/u)?.[1],
      write: (text, version) => text.replace(/AppVersion\s*=\s*"[^"]+"/u, `AppVersion = "${version}"`),
    },
  ]
}

export function readVersion(root) {
  const source = versionTargets()[0]
  return source.read(readFileSync(join(root, source.file), 'utf8'))
}

export function writeVersion(root, version) {
  assertPatchOnly(version)
  const changed = []
  for (const target of versionTargets()) {
    const path = join(root, target.file)
    const before = readFileSync(path, 'utf8')
    const after = target.write(before, version)
    if (after === before) continue
    writeFileSync(path, after, 'utf8')
    changed.push(target.file)
  }
  return changed
}

export function assertPatchOnly(version) {
  if (!/^1\.0\.\d+$/u.test(version ?? '')) {
    throw new Error(`Releases are patch-only: expected 1.0.x, got ${version ?? 'nothing'}`)
  }
}

export function nextPatch(version) {
  assertPatchOnly(version)
  return `1.0.${Number(version.split('.')[2]) + 1}`
}

// A release whose surfaces disagree ships an interface, an installer and a
// checksum that describe different builds, so the build fails instead.
export function assertVersionsAgree(root, expected) {
  assertPatchOnly(expected)
  const mismatched = versionTargets()
    .map((target) => ({ file: target.file, found: target.read(readFileSync(join(root, target.file), 'utf8')) }))
    .filter((target) => target.found !== expected)
  if (mismatched.length === 0) return
  const detail = mismatched.map((target) => `${target.file}: ${target.found ?? 'missing'}`).join(', ')
  throw new Error(`Version ${expected} disagrees with ${detail}. Run: pnpm version:set ${expected}`)
}

// replaceJSONVersion keeps the manifest's own formatting and key order: these
// files are hand maintained, so re-serialising would reformat unrelated lines.
function replaceJSONVersion(text, version) {
  return text.replace(/("version"\s*:\s*)"[^"]+"/u, `$1"${version}"`)
}
