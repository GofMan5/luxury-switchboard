import assert from 'node:assert/strict'
import { existsSync, mkdirSync, mkdtempSync, rmSync, utimesSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import test from 'node:test'
import { collectBundle, removeStaleBundles } from './release-bundles.mjs'

const version = '1.0.58'
const windows = { bundle: 'nsis', suffix: '.exe' }

function fixture(t) {
  const directory = mkdtempSync(join(tmpdir(), 'switchboard-release-fixture-'))
  assert.equal(resolve(directory), directory)
  assert.ok(directory.startsWith(join(tmpdir(), 'switchboard-release-fixture-')))
  t.after(() => rmSync(directory, { recursive: true, force: true }))
  return directory
}

function artifact(directory, name, mtime = Date.now() + 60_000) {
  const path = join(directory, name)
  writeFileSync(path, 'synthetic installer bytes')
  utimesSync(path, mtime / 1000, mtime / 1000)
  return path
}

test('cleans future-dated native underscore bundles for current and legacy product names', (t) => {
  const directory = fixture(t)
  const names = [
    'Luxury Switchboard_1.0.58_x64-setup.exe',
    'Provider Switchboard_1.0.58_x64-setup.exe',
    'Luxury-Switchboard-1.0.58-windows-x64-setup.exe',
  ]
  for (const name of names) artifact(directory, name)
  const neighbour = artifact(directory, 'Luxury Switchboard_1.0.580_x64-setup.exe')
  const previous = artifact(directory, 'Luxury Switchboard_1.0.57_x64-setup.exe')
  const otherType = artifact(directory, 'Luxury Switchboard_1.0.58_x64.deb')
  const namedDirectory = join(directory, 'Not-a-file_1.0.58_x64-setup.exe')
  mkdirSync(namedDirectory)
  removeStaleBundles(directory, windows, version)
  for (const name of names) assert.equal(existsSync(join(directory, name)), false, `${name} survived cleanup`)
  for (const path of [neighbour, previous, otherType, namedDirectory]) assert.equal(existsSync(path), true)
  assert.throws(() => collectBundle(directory, windows, version, Date.now()), /found 0/u)
})

test('never collects neighbour patch 1.0.580 even with a future mtime', (t) => {
  const directory = fixture(t)
  artifact(directory, 'Luxury Switchboard_1.0.580_x64-setup.exe')
  assert.throws(() => collectBundle(directory, windows, version, Date.now()), /found 0/u)
  const current = artifact(directory, 'Luxury Switchboard_1.0.58_x64-setup.exe')
  assert.equal(collectBundle(directory, windows, version, Date.now()), current)
})

test('cleanup and collection use the same native version fields for every bundle type', (t) => {
  for (const [bundle, suffix, name] of [
    ['nsis', '.exe', 'Luxury Switchboard_1.0.58_arm64-setup.exe'],
    ['deb', '.deb', 'luxury-switchboard_1.0.58_amd64.deb'],
    ['appimage', '.AppImage', 'Luxury Switchboard_1.0.58_x86_64.AppImage'],
    ['appimage', '.AppImage', 'Luxury-Switchboard-1.0.58-linux-x86_64.AppImage'],
    ['dmg', '.dmg', 'Luxury Switchboard_1.0.58_aarch64.dmg'],
    ['dmg', '.dmg', 'Luxury-Switchboard-1.0.58-macos-arm64.dmg'],
  ]) {
    const directory = fixture(t)
    const descriptor = { bundle, suffix }
    const startedAt = Date.now()
    artifact(directory, name)
    removeStaleBundles(directory, descriptor, version)
    assert.throws(() => collectBundle(directory, descriptor, version, startedAt), /found 0/u)
    const fresh = artifact(directory, name)
    assert.equal(collectBundle(directory, descriptor, version, startedAt), fresh)
  }
})

test('ignores older writes and refuses ambiguous freshly-built output', (t) => {
  const directory = fixture(t)
  const startedAt = Date.now()
  artifact(directory, 'Luxury Switchboard_1.0.58_x64-setup.exe', startedAt - 1000)
  assert.throws(() => collectBundle(directory, windows, version, startedAt), /found 0/u)
  const fresh = artifact(directory, 'Luxury Switchboard_1.0.58_arm64-setup.exe')
  assert.equal(collectBundle(directory, windows, version, startedAt), fresh)
  artifact(directory, 'Provider Switchboard_1.0.58_arm64-setup.exe')
  assert.throws(() => collectBundle(directory, windows, version, startedAt), /found 2/u)
})
