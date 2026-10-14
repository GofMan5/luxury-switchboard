import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import { readVersion, versionTargets } from './version.mjs'

const workspace = dirname(dirname(fileURLToPath(import.meta.url)))

test('checksum collection replaces the stale root manifest used by friend packaging', (t) => {
  const root = mkdtempSync(join(tmpdir(), 'switchboard-checksum-fixture-'))
  assert.equal(resolve(root), root)
  assert.ok(root.startsWith(join(tmpdir(), 'switchboard-checksum-fixture-')))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  for (const name of [...versionTargets().map((target) => target.file), 'scripts/build-release.mjs', 'scripts/release-bundles.mjs', 'scripts/version.mjs']) {
    const destination = join(root, name)
    mkdirSync(dirname(destination), { recursive: true })
    copyFileSync(join(workspace, name), destination)
  }
  const directory = join(root, 'artifacts', 'release')
  mkdirSync(directory, { recursive: true })
  const name = `Luxury-Switchboard-${readVersion(root)}-windows-x64-setup.exe`
  const body = 'synthetic installer bytes only'
  writeFileSync(join(directory, name), body)
  writeFileSync(join(root, 'SHA256SUMS.txt'), 'stale previous release manifest\n')
  const result = spawnSync(process.execPath, [join(root, 'scripts', 'build-release.mjs'), '--checksums-only'], { encoding: 'utf8', timeout: 10_000 })
  assert.ifError(result.error)
  assert.equal(result.status, 0, result.stderr)
  const expected = `${createHash('sha256').update(body).digest('hex').toUpperCase()}  ${name}\n`
  assert.equal(readFileSync(join(directory, 'SHA256SUMS.txt'), 'utf8'), expected)
  assert.equal(readFileSync(join(root, 'SHA256SUMS.txt'), 'utf8'), expected)
})
