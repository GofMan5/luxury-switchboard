// Fails when the five version surfaces disagree or the version is not a patch
// release, so `pnpm check` catches a drifted version long before a release build
// spends twenty minutes discovering it.
import { dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

import { assertVersionsAgree, readVersion } from './version.mjs'

const workspace = dirname(dirname(fileURLToPath(import.meta.url)))
const version = readVersion(workspace)
assertVersionsAgree(workspace, version)
console.log(`Version ${version} agrees across every surface`)
