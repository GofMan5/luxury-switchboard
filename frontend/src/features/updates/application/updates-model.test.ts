import { describe, expect, it, vi } from 'vitest'
import { UpdatesModel } from './updates-model'
import type { UpdateCheck, UpdateInstall, UpdateInstallProgress } from '../domain/update'

function port(answer: UpdateCheck, install: UpdateInstall = { path: 'C:\\update\\setup.exe', version: '1.0.39' }) {
  return {
    check: vi.fn(async () => answer),
    install: vi.fn(async () => install),
    onInstallProgress: vi.fn((_: (report: UpdateInstallProgress) => void) => () => undefined),
  }
}

const newer: UpdateCheck = { current: '1.0.38', latest: '1.0.39', url: 'https://example.test/rel', newer: true, reachable: true, checkedAt: '2026-09-29T12:00:00Z' }

describe('UpdatesModel', () => {
  it('publishes the check the backend answered', async () => {
    const updates = new UpdatesModel(port(newer))
    await updates.check()
    expect(updates.snapshot().check?.newer).toBe(true)
    expect(updates.snapshot().check?.latest).toBe('1.0.39')
  })

  it('stays silent when the check itself fails', async () => {
    const updates = new UpdatesModel({ check: vi.fn(async () => { throw new Error('offline') }), install: vi.fn(), onInstallProgress: vi.fn(() => () => undefined) })
    await updates.check()
    expect(updates.snapshot().check).toBeNull()
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

  it('a second click while one runs is ignored, not queued', async () => {
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
