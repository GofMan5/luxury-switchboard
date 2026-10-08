import { describe, expect, it, vi } from 'vitest'
import { UpdatesModel } from './updates-model'
import type { UpdateCheck, UpdateInstall, UpdateInstallProgress } from '../domain/update'

/** The microtask chain behind a seeded status answer needs a macrotask to
 * settle; one setTimeout tick flushes it without fake timers. */
const flush = () => new Promise<void>((resolve) => { setTimeout(resolve, 0) })

function port(answer: UpdateCheck, install: UpdateInstall = { path: 'C:\\update\\setup.exe', version: '1.0.39' }) {
  return {
    check: vi.fn(async () => answer),
    status: vi.fn(async () => answer),
    install: vi.fn(async () => install),
    onInstallProgress: vi.fn((_: (report: UpdateInstallProgress) => void) => () => undefined),
    onStateChanged: vi.fn((_: (check: UpdateCheck) => void) => () => undefined),
  }
}

const newer: UpdateCheck = { current: '1.0.38', latest: '1.0.39', url: 'https://example.test/rel', newer: true, reachable: true, checkedAt: '2026-09-29T12:00:00Z' }

describe('UpdatesModel', () => {
  it('seeds the state from the backend’s own answer, not a new round trip', async () => {
    const stub = port(newer)
    const updates = new UpdatesModel(stub)
    updates.connect()
    await flush()
    expect(updates.snapshot().check?.latest).toBe('1.0.39')
    expect(stub.check).not.toHaveBeenCalled()
  })

  it('stays silent when the backend has not answered yet', async () => {
    const stub = port(newer)
    stub.status = vi.fn(async () => { throw new Error('no answer yet') })
    const updates = new UpdatesModel(stub)
    updates.connect()
    await flush()
    // A status that cannot answer is not the operator's transaction: no
    // error surface, and the first stateChanged event brings the answer.
    expect(updates.snapshot().check).toBeNull()
    expect(updates.snapshot().checkError).toBe('')
  })

  it('turns the verdict the minute the backend announces it', async () => {
    const stub = port(newer)
    const updates = new UpdatesModel(stub)
    updates.connect()
    const stateListener = stub.onStateChanged.mock.calls[0]?.[0]
    if (!stateListener) throw new Error('connect() did not subscribe to state changes')
    const settled: UpdateCheck = { ...newer, newer: false, latest: '1.0.38', checkedAt: '2026-09-29T13:00:00Z' }
    stateListener(settled)
    expect(updates.snapshot().check?.newer).toBe(false)
    expect(updates.snapshot().check?.checkedAt).toBe('2026-09-29T13:00:00Z')
  })

  it('publishes the check the backend answered', async () => {
    const updates = new UpdatesModel(port(newer))
    await updates.check()
    expect(updates.snapshot().check?.newer).toBe(true)
    expect(updates.snapshot().check?.latest).toBe('1.0.39')
    expect(updates.snapshot().checking).toBe(false)
  })

  it('a failed manual check says why, and only where it was asked', async () => {
    const stub = port(newer)
    stub.check = vi.fn(async () => { throw new Error('the release feed is unreachable') })
    const updates = new UpdatesModel(stub)
    await updates.check()
    expect(updates.snapshot().check).toBeNull()
    expect(updates.snapshot().checkError).toContain('unreachable')
    expect(updates.snapshot().checking).toBe(false)
  })

  it('a manual check in flight says so and stops taking clicks', async () => {
    let release: ((value: UpdateCheck) => void) | undefined
    const stub = port(newer)
    stub.check = vi.fn(() => new Promise<UpdateCheck>((resolve) => { release = resolve }))
    const updates = new UpdatesModel(stub)
    updates.connect()
    await flush()
    const first = updates.check()
    expect(updates.snapshot().checking).toBe(true)
    await updates.check() // a second click while one runs is ignored, not queued
    expect(stub.check).toHaveBeenCalledTimes(1)
    release?.(newer)
    await first
    expect(updates.snapshot().checking).toBe(false)
    expect(updates.snapshot().check?.latest).toBe('1.0.39')
  })

  it('tracks the download and lands on the verified path', async () => {
    const stub = port(newer)
    const updates = new UpdatesModel(stub)
    updates.connect()
    // The progress subscription the connect() wired is the one the backend
    // feeds; report one event and see it on the state.
    const reported: UpdateInstallProgress = { phase: 'downloading', received: 600, total: 1_200, percent: 50 }
    const progressListener = stub.onInstallProgress.mock.calls[0]?.[0]
    if (!progressListener) throw new Error('connect() did not subscribe to install progress')
    progressListener(reported)
    expect(updates.snapshot().installPhase).toBe('downloading')
    expect(updates.snapshot().installPercent).toBe(50)

    const ok = await updates.install()
    expect(ok).toBe(true)
    expect(updates.snapshot().installPhase).toBe('ready')
    expect(updates.snapshot().installerPath).toBe('C:\\update\\setup.exe')
  })

  it('a refused download says so instead of a frozen bar', async () => {
    const stub = port(newer)
    stub.install = vi.fn(async () => { throw new Error('the installer failed its checksum') })
    const updates = new UpdatesModel(stub)
    updates.connect()
    const ok = await updates.install()
    expect(ok).toBe(false)
    expect(updates.snapshot().installPhase).toBe('error')
    expect(updates.snapshot().installError).toContain('checksum')
  })

  it('a second install click while one runs is ignored, not queued', async () => {
    let release: (() => void) | undefined
    const stub = port(newer)
    stub.install = vi.fn(() => new Promise<UpdateInstall>((resolve) => { release = () => resolve({ path: 'p', version: '1' }) }))
    const updates = new UpdatesModel(stub)
    updates.connect()
    const first = updates.install()
    const second = await updates.install()
    expect(second).toBe(false)
    expect(stub.install).toHaveBeenCalledTimes(1)
    release?.()
    await first
    expect(updates.snapshot().installPhase).toBe('ready')
  })
})
