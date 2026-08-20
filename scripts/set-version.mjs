// Sets the release version in every place that states it, so the number is
// typed once and the UI, installer and checksums can never drift apart.
//
//   pnpm version:set 1.0.8   set an explicit version
//   pnpm version:set         bump the patch by one
import { dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

import { nextPatch, readVersion, writeVersion } from './version.mjs'

const workspace = dirname(dirname(fileURLToPath(import.meta.url)))
const current = readVersion(workspace)
const requested = process.argv[2] ?? nextPatch(current)
const changed = writeVersion(workspace, requested)

for (const file of changed) console.log(`${file}: ${requested}`)
console.log(changed.length === 0 ? `Already at ${requested}` : `Version set to ${requested} (was ${current})`)
