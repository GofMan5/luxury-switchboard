// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { CodexAccount, CodexQuotaReport } from '../domain/codex'
import type { CodexModelState, CodexQuotaCard } from '../application/codex-model'
import CodexAccountsPane from './CodexAccountsPane'

// The pane reads the shared Codex state through useCodex and owns exactly one
// model action — the per-account usage probe — while sign-in and sign-out are
// the host page's to run, so the pane only receives callbacks to ask. The
// model object is created once, exactly like the real service instance the
// app hands out, so effect dependencies stay stable across re-renders.
const mocks = vi.hoisted(() => {
  const refreshQuota = vi.fn()
  return {
    state: null as unknown as CodexModelState,
    refreshQuota,
    model: { refreshQuota },
    onAddAccount: vi.fn(),
    onSignIn: vi.fn(),
    onSignOut: vi.fn(),
  }
})
vi.mock('./useCodex', () => ({
  useCodex: () => ({
    model: mocks.model,
    state: mocks.state,
  }),
}))

// Auto-cleanup needs vitest globals, which this project does not enable.
afterEach(cleanup)

beforeEach(() => {
  mocks.refreshQuota.mockReset()
  mocks.refreshQuota.mockResolvedValue(undefined)
  mocks.onAddAccount.mockReset()
  mocks.onSignIn.mockReset()
  mocks.onSignOut.mockReset()
  mocks.state = stateAt({})
})

function accountAt(overrides: Partial<CodexAccount>): CodexAccount {
  return { state: 'signed_out', email: '', plan: '', accountId: '', providerId: '', ...overrides }
}

function quotaAt(overrides: Partial<CodexQuotaReport>): CodexQuotaReport {
  return {
    fetchedAt: 1_789_000_000,
    planType: 'Pro',
    primary: { present: true, remainingPercent: 78, windowMinutes: 300, resetAt: 1_789_003_120 },
    secondary: { present: true, remainingPercent: 41, windowMinutes: 10_080, resetAt: 1_789_172_800 },
    ...overrides,
  }
}

function cardAt(overrides: Partial<CodexQuotaCard>): CodexQuotaCard {
  return { quota: null, error: '', pending: false, ...overrides }
}

function stateAt(overrides: Partial<CodexModelState>): CodexModelState {
  return {
    loginPhase: 'idle',
    activeMethod: null,
    state: 'signed_out',
    accounts: [],
    freshAccount: null,
    loginError: '',
    authorizeUrl: '',
    deviceUserCode: '',
    deviceVerificationUrl: '',
    importedFrom: '',
    quotas: {},
    logoutError: '',
    ...overrides,
  }
}

const devAccount = accountAt({ state: 'signed_in', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex' })
const samAccount = accountAt({ state: 'signed_in', email: 'sam@example.com', plan: 'Plus', accountId: 'acct-2', providerId: 'codex' })
const reauthAccount = accountAt({ state: 'reauth_needed', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex' })
const signedOutAccount = accountAt({ state: 'signed_out', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex' })

const devQuota = quotaAt({})
const samQuota = quotaAt({
  planType: 'Plus',
  primary: { present: true, remainingPercent: 23, windowMinutes: 300, resetAt: 1_789_003_120 },
  secondary: { present: true, remainingPercent: 64, windowMinutes: 10_080, resetAt: 1_789_172_800 },
})

function renderPane(state: CodexModelState, canSignIn = true) {
  mocks.state = state
  return render(
    <CodexAccountsPane
      canSignIn={canSignIn}
      onAddAccount={mocks.onAddAccount}
      onSignIn={mocks.onSignIn}
      onSignOut={mocks.onSignOut}
    />,
  )
}

function meter(label: string | RegExp) {
  return screen.getByRole('meter', { name: label }) as HTMLElement
}

function rowButton(email: string) {
  return screen.getByRole('button', { name: `Refresh usage for ${email}` }) as HTMLButtonElement
}

describe('CodexAccountsPane', () => {
  it('renders every account as its own row in the cockpit grid', () => {
    renderPane(stateAt({
      accounts: [devAccount, samAccount],
      quotas: { 'acct-1': cardAt({ quota: devQuota }), 'acct-2': cardAt({ quota: samQuota }) },
    }))

    // The grid announces itself as one named region with a stable column set.
    expect(screen.getByRole('region', { name: 'Codex accounts and usage' })).toBeTruthy()
    for (const header of ['Account', 'State', '5h window', 'Weekly window', 'Updated', 'Actions']) {
      expect(screen.getByRole('columnheader', { name: header })).toBeTruthy()
    }

    expect(screen.getByText('dev@example.com')).toBeTruthy()
    expect(screen.getByText('sam@example.com')).toBeTruthy()
    expect(screen.getByText('Pro')).toBeTruthy()
    expect(screen.getByText('Plus')).toBeTruthy()
    expect(screen.getAllByText('Signed in')).toHaveLength(2)
    expect(meter('5h window, 78% remaining').getAttribute('aria-valuenow')).toBe('78')
    expect(meter('5h window, 23% remaining').getAttribute('aria-valuenow')).toBe('23')
    expect(meter('Weekly window, 41% remaining').getAttribute('aria-valuenow')).toBe('41')
    expect(meter('Weekly window, 64% remaining').getAttribute('aria-valuenow')).toBe('64')
    expect(screen.getByText('78% left')).toBeTruthy()
    expect(screen.getByText('23% left')).toBeTruthy()
    expect(screen.getAllByText(/^Resets /)).toHaveLength(4)
    expect(screen.getAllByText(/^Updated /)).toHaveLength(2)
    // Both rows already carry usage, so mount probes nothing.
    expect(mocks.refreshQuota).not.toHaveBeenCalled()
  })

  it('probes each signed-in account once on mount', () => {
    renderPane(stateAt({ accounts: [devAccount, samAccount] }))

    expect(mocks.refreshQuota).toHaveBeenCalledTimes(2)
    expect(mocks.refreshQuota).toHaveBeenCalledWith('acct-1')
    expect(mocks.refreshQuota).toHaveBeenCalledWith('acct-2')
  })

  it('skips accounts that are not signed in when probing', () => {
    renderPane(stateAt({ accounts: [devAccount, reauthAccount] }))

    expect(mocks.refreshQuota).toHaveBeenCalledTimes(1)
    expect(mocks.refreshQuota).toHaveBeenCalledWith('acct-1')
  })

  it('probes only the newly signed-in account, not rows that already have usage', () => {
    const view = renderPane(stateAt({ accounts: [devAccount] }))
    expect(mocks.refreshQuota).toHaveBeenCalledWith('acct-1')

    mocks.state = stateAt({
      accounts: [devAccount, samAccount],
      quotas: { 'acct-1': cardAt({ quota: devQuota }) },
    })
    view.rerender(
      <CodexAccountsPane
        canSignIn
        onAddAccount={mocks.onAddAccount}
        onSignIn={mocks.onSignIn}
        onSignOut={mocks.onSignOut}
      />,
    )

    expect(mocks.refreshQuota).toHaveBeenCalledTimes(2)
    expect(mocks.refreshQuota).toHaveBeenCalledWith('acct-2')
  })

  it('retries a failed row when the signed-in set changes and never loops', () => {
    const failed = cardAt({ error: 'The Codex usage could not be loaded.' })
    const view = renderPane(stateAt({ accounts: [devAccount], quotas: { 'acct-1': failed } }))
    expect(mocks.refreshQuota).toHaveBeenCalledWith('acct-1')

    mocks.state = stateAt({ accounts: [devAccount, samAccount], quotas: { 'acct-1': failed } })
    view.rerender(
      <CodexAccountsPane
        canSignIn
        onAddAccount={mocks.onAddAccount}
        onSignIn={mocks.onSignIn}
        onSignOut={mocks.onSignOut}
      />,
    )
    expect(mocks.refreshQuota).toHaveBeenCalledTimes(3)
    expect(mocks.refreshQuota).toHaveBeenCalledWith('acct-2')

    // Same set again: the effect does not re-fire, so no probe storm.
    view.rerender(
      <CodexAccountsPane
        canSignIn
        onAddAccount={mocks.onAddAccount}
        onSignIn={mocks.onSignIn}
        onSignOut={mocks.onSignOut}
      />,
    )
    expect(mocks.refreshQuota).toHaveBeenCalledTimes(3)
  })

  it('refreshes one row without probing its siblings', () => {
    renderPane(stateAt({
      accounts: [devAccount, samAccount],
      quotas: { 'acct-1': cardAt({ quota: devQuota }), 'acct-2': cardAt({ quota: samQuota }) },
    }))
    mocks.refreshQuota.mockClear()

    fireEvent.click(rowButton('sam@example.com'))

    expect(mocks.refreshQuota).toHaveBeenCalledTimes(1)
    expect(mocks.refreshQuota).toHaveBeenCalledWith('acct-2')
  })

  it('keeps the last good windows beside a failed probe\'s alert', () => {
    renderPane(stateAt({
      accounts: [devAccount],
      quotas: {
        'acct-1': cardAt({
          quota: quotaAt({ secondary: { present: false, remainingPercent: 100 } }),
          error: 'The Codex usage could not be loaded.',
        }),
      },
    }))

    expect(screen.getByRole('alert').textContent).toBe('The Codex usage could not be loaded.')
    expect(meter('5h window, 78% remaining').getAttribute('aria-valuenow')).toBe('78')
    // The unreported window renders no row at all.
    expect(screen.queryByRole('meter', { name: /Weekly window/ })).toBeNull()
  })

  it('shows the checking state on its own row while a probe is in flight', () => {
    renderPane(stateAt({
      accounts: [devAccount, samAccount],
      quotas: { 'acct-1': cardAt({ pending: true }), 'acct-2': cardAt({ quota: samQuota }) },
    }))

    const busy = rowButton('dev@example.com')
    expect(busy.disabled).toBe(true)
    expect(screen.getByText('Checking usage…')).toBeTruthy()
    // The sibling row keeps working while this one waits.
    expect(rowButton('sam@example.com').disabled).toBe(false)
  })

  it('answers a reauth row with the expiry hint, no meters, and the row actions that ask the page', () => {
    renderPane(stateAt({ accounts: [reauthAccount] }))

    expect(screen.getByText('Sign-in needed')).toBeTruthy()
    expect(screen.getByText('The session expired. Sign in again and the usage windows return.')).toBeTruthy()
    expect(screen.queryByRole('meter')).toBeNull()
    expect(screen.queryByRole('alert')).toBeNull()
    expect(rowButton('dev@example.com').disabled).toBe(true)
    // No live session, so mount did not probe.
    expect(mocks.refreshQuota).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: 'Sign in again for dev@example.com' }))
    expect(mocks.onSignIn).toHaveBeenCalledTimes(1)

    fireEvent.click(screen.getByRole('button', { name: 'Sign out dev@example.com' }))
    expect(mocks.onSignOut).toHaveBeenCalledTimes(1)
    expect(mocks.onSignOut).toHaveBeenCalledWith(reauthAccount)
  })

  it('answers a signed-out row with the sign-out hint and no sign-out button', () => {
    renderPane(stateAt({ accounts: [signedOutAccount] }))

    expect(screen.getByText('Signed out')).toBeTruthy()
    expect(screen.getByText('This account is signed out. Sign in again to bring it back.')).toBeTruthy()
    expect(screen.queryByRole('meter')).toBeNull()
    expect(rowButton('dev@example.com').disabled).toBe(true)
    expect(mocks.refreshQuota).not.toHaveBeenCalled()
    // Still worth renewing, but nothing to unlink.
    fireEvent.click(screen.getByRole('button', { name: 'Sign in again for dev@example.com' }))
    expect(mocks.onSignIn).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('button', { name: 'Sign out dev@example.com' })).toBeNull()
  })

  it('hides the row sign-in action when the build cannot sign in', () => {
    renderPane(stateAt({ accounts: [reauthAccount] }), false)

    expect(screen.queryByRole('button', { name: 'Sign in again for dev@example.com' })).toBeNull()
    // The session can still be unlinked without the sign-in capability.
    expect(screen.getByRole('button', { name: 'Sign out dev@example.com' })).toBeTruthy()
  })

  it('answers no accounts with the empty state whose Add account asks the page', () => {
    renderPane(stateAt({}))

    expect(screen.getByText('No Codex accounts yet')).toBeTruthy()
    expect(screen.getByText('Add a ChatGPT account and its usage windows appear here.')).toBeTruthy()
    expect(screen.queryByRole('meter')).toBeNull()
    expect(mocks.refreshQuota).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: 'Add account' }))
    expect(mocks.onAddAccount).toHaveBeenCalledTimes(1)
  })

  it('offers no empty-state button when the build cannot sign in', () => {
    renderPane(stateAt({}), false)

    expect(screen.getByText('No Codex accounts yet')).toBeTruthy()
    expect(screen.getByText('Adding Codex accounts is not available in this build.')).toBeTruthy()
    expect(screen.queryByRole('button')).toBeNull()
    expect(mocks.refreshQuota).not.toHaveBeenCalled()
  })

  it('says usage is not loaded yet for a signed-in row without a probe', () => {
    renderPane(stateAt({ accounts: [devAccount], quotas: {} }))

    expect(screen.getByText('No usage loaded yet. Use Refresh usage.')).toBeTruthy()
    expect(mocks.refreshQuota).toHaveBeenCalledWith('acct-1')
  })

  it('says no windows were reported when the probe answered without them', () => {
    renderPane(stateAt({
      accounts: [devAccount],
      quotas: {
        'acct-1': cardAt({
          quota: quotaAt({
            primary: { present: false, remainingPercent: 100 },
            secondary: { present: false, remainingPercent: 100 },
          }),
        }),
      },
    }))

    expect(screen.getByText('No usage windows reported for this account.')).toBeTruthy()
    expect(screen.queryByRole('meter')).toBeNull()
  })
})
