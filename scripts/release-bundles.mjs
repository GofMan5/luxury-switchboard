import { existsSync, readdirSync, statSync, unlinkSync } from 'node:fs'
import { join } from 'node:path'

// Tauri uses native _version_ names (NSIS/deb) and -version- names
// (AppImage/dmg); match the whole field, never a neighbouring patch.
export function matchesBundle(name, artifact, version) {
  return name.endsWith(artifact.suffix)
    && (name.includes(`_${version}_`) || name.includes(`-${version}-`))
}

export function removeStaleBundles(directory, artifact, version) {
  if (!existsSync(directory)) return
  for (const name of readdirSync(directory)) {
    const path = join(directory, name)
    if (matchesBundle(name, artifact, version) && statSync(path).isFile()) unlinkSync(path)
  }
}

export function collectBundle(directory, artifact, version, startedAt) {
  const candidates = readdirSync(directory)
    .filter((name) => matchesBundle(name, artifact, version))
    .map((name) => join(directory, name))
    .filter((path) => {
      const entry = statSync(path)
      return entry.isFile() && entry.mtimeMs >= startedAt
    })
  if (candidates.length !== 1) {
    throw new Error(`Expected one freshly built ${artifact.bundle} artifact for ${version}, found ${candidates.length}`)
  }
  return candidates[0]
}
