// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { UpdateCheck } from '../domain/update'
import type { UpdatesState } from '../application/updates-model'
import { UpdateDialog } from './UpdateDialog'

const mocks = vi.hoisted(() => ({
  runInstaller: vi.fn(),
  openExternal: vi.fn(),
}))
vi.mock('../../../platform/lifecycle/run-installer', () => ({ runInstaller: mocks.runInstaller }))
vi.mock('../../../platform/lifecycle/open-external', () => ({ openExternal: mocks.openExternal }))

// Auto-cleanup needs vitest globals, which this project does not enable. Without
// this the previous dialog stays mounted and role queries match stale buttons.
afterEach(cleanup)

// The mocks live at module scope, so call history would leak across tests.
beforeEach(() => {
  mocks.runInstaller.mockReset()
  mocks.openExternal.mockReset()
})

const check: UpdateCheck = {
  current: '1.0.44',
  latest: '1.0.45',
  url: 'https://github.com/GofMan5/luxury-switchboard/releases/tag/v1.0.45',
  newer: true,
  reachable: true,
  checkedAt: '2026-09-30T00:00:00Z',
}

const installerPath = 'C:\\Users\\dev\\AppData\\Local\\ProviderSwitchboard\\update\\Luxury-Switchboard-1.0.99-windows-x64-setup.exe'

function stateAt(overrides: Partial<UpdatesState>): UpdatesState {
  return {
    check: null,
    installPhase: 'idle',
    installPercent: 0,
    installerPath: '',
    installError: '',
    ...overrides,
  }
}

function renderDialog(state: UpdatesState, onInstall = vi.fn(async () => true)) {
  return render(
    <UpdateDialog check={check} state={state} onClose={vi.fn()} onInstall={onInstall} />,
  )
}

describe('UpdateDialog', () => {
  it('says the shell refusal out loud and keeps the verified file visible, even when the shell rejects with a bare string', async () => {
    mocks.runInstaller.mockRejectedValueOnce('the installer is not in the update directory')
    renderDialog(stateAt({ installPhase: 'ready', installerPath }))

    fireEvent.click(screen.getByRole('button', { name: 'Restart and install' }))
    expect(mocks.runInstaller).toHaveBeenCalledWith(installerPath, true)

    // The Rust command rejects with plain strings, not Error: the dialog has
    // to read both shapes or the operator gets "undefined" for a reason.
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('the installer is not in the update directory')
    expect(alert.textContent).toContain('It is verified and on disk:')
    expect(screen.getByText(installerPath)).toBeTruthy()
  })

  it('carries the browser-shell refusal (an Error) the same way as a shell refusal', async () => {
    mocks.runInstaller.mockRejectedValueOnce(new Error('the browser shell has no desktop runtime to start installers'))
    renderDialog(stateAt({ installPhase: 'ready', installerPath }))

    fireEvent.click(screen.getByRole('button', { name: 'Restart and install' }))
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('no desktop runtime to start installers')
    expect(screen.getByText(installerPath)).toBeTruthy()
  })

  // A deb install on Linux cannot self-replace, and the shell says so by naming
  // the release page as the path. Naming the page changes the whole answer:
  // no second "path" (the file line), no restart that would just refuse again.
  it('drops the file line and the restart, and lets the release page lead, when the shell refusal names the release page (deb Linux)', async () => {
    mocks.runInstaller.mockRejectedValueOnce('this install was not started from an AppImage; the release page is the path')
    renderDialog(stateAt({ installPhase: 'ready', installerPath }))

    fireEvent.click(screen.getByRole('button', { name: 'Restart and install' }))
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('this install was not started from an AppImage')
    expect(alert.textContent).not.toContain('It is verified and on disk')
    expect(screen.queryByText(installerPath)).toBeNull()
    expect(screen.queryByRole('button', { name: 'Restart and install' })).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Release page' }))
    expect(mocks.openExternal).toHaveBeenCalledWith(check.url)
  })

  it('drops the file line and the restart, and lets the release page lead, when the shell refusal names the release page (bundle-less macOS)', async () => {
    mocks.runInstaller.mockRejectedValueOnce('this install was not started from the app bundle; the release page is the path')
    renderDialog(stateAt({ installPhase: 'ready', installerPath }))

    fireEvent.click(screen.getByRole('button', { name: 'Restart and install' }))
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('this install was not started from the app bundle')
    expect(alert.textContent).not.toContain('It is verified and on disk')
    expect(screen.queryByText(installerPath)).toBeNull()
    expect(screen.queryByRole('button', { name: 'Restart and install' })).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Release page' }))
    expect(mocks.openExternal).toHaveBeenCalledWith(check.url)
  })

  it('offers no retry when the release ships no installer for this platform, and the release page leads', () => {
    renderDialog(stateAt({
      installPhase: 'error',
      installError: 'The release ships no self-update for this platform; the release page is the path.',
    }))

    expect(screen.queryByRole('button', { name: 'Try again' })).toBeNull()
    const release = screen.getByRole('button', { name: 'Release page' })
    fireEvent.click(release)
    expect(mocks.openExternal).toHaveBeenCalledWith(check.url)
  })

  it('keeps retry as the primary action for an ordinary download failure', () => {
    const onInstall = vi.fn(async () => true)
    renderDialog(stateAt({ installPhase: 'error', installError: 'The update could not be downloaded.' }), onInstall)

    fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
    expect(onInstall).toHaveBeenCalledOnce()
    expect(mocks.openExternal).not.toHaveBeenCalled()
  })
})
