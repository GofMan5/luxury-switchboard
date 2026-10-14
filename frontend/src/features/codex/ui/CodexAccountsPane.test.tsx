// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { CodexAccount, CodexQuotaReport } from '../domain/codex'
import type { CodexModelState, CodexQuotaCard } from '../application/codex-model'
import CodexAccountsPane from './CodexAccountsPane'

// The pane reads the shared Codex state through useCodex and owns exactly
// three model actions — the per-account usage probe, the fleet or selection
// wake, and the fleet usage refresh — while sign-in, sign-out and removal are
// the host page's to run, so the pane only receives callbacks to ask. The
// model object is created once, exactly like the real service instance the
// app hands out, so effect dependencies stay stable across re-renders.
const mocks = vi.hoisted(() => {
  const refreshQuota = vi.fn()
  const wake = vi.fn()
  const refreshAll = vi.fn()
  return {
    state: null as unknown as CodexModelState,
    refreshQuota,
    wake,
    refreshAll,
    model: { refreshQuota, wake, refreshAll },
    onAddAccount: vi.fn(),
    onSignIn: vi.fn(),
    onSignOut: vi.fn(),
    onRemove: vi.fn(),
  }
})
vi.mock('./useCodex', () => ({
  useCodex: () => ({
    model: mocks.model,
    state: mocks.state,
  }),
}))

// Auto-cleanup needs vitest globals, which this project does not enable.
afterEach(() => { cleanup(); vi.useRealTimers() })

beforeEach(() => {
  mocks.refreshQuota.mockReset()
  mocks.refreshQuota.mockResolvedValue(undefined)
  mocks.wake.mockReset()
  mocks.wake.mockResolvedValue(undefined)
  mocks.refreshAll.mockReset()
  mocks.refreshAll.mockResolvedValue(undefined)
  mocks.onAddAccount.mockReset()
  mocks.onSignIn.mockReset()
  mocks.onSignOut.mockReset()
  mocks.onRemove.mockReset()
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
    wakingIds: [],
    probingAll: false,
    wakeError: '',
    ...overrides,
  }
}

const devAccount = accountAt({ state: 'signed_in', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex', loginMethod: 'device', resets: 2 })
const samAccount = accountAt({ state: 'signed_in', email: 'sam@example.com', plan: 'Plus', accountId: 'acct-2', providerId: 'codex', loginMethod: 'browser', resets: 5 })
const reauthAccount = accountAt({ state: 'reauth_needed', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex' })
const reauthSamAccount = accountAt({ state: 'reauth_needed', email: 'sam@example.com', plan: 'Plus', accountId: 'acct-2', providerId: 'codex' })
const signedOutAccount = accountAt({ state: 'signed_out', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex' })

const devQuota = quotaAt({})
const samQuota = quotaAt({
  planType: 'Plus',
  primary: { present: true, remainingPercent: 23, windowMinutes: 300, resetAt: 1_789_003_120 },
  secondary: { present: true, remainingPercent: 64, windowMinutes: 10_080, resetAt: 1_789_172_800 },
})

const twoQuotas = {
  'acct-1': cardAt({ quota: devQuota }),
  'acct-2': cardAt({ quota: samQuota }),
}

function paneProps() {
  return {
    onAddAccount: mocks.onAddAccount,
    onSignIn: mocks.onSignIn,
    onSignOut: mocks.onSignOut,
    onRemove: mocks.onRemove,
  }
}

function renderPane(state: CodexModelState, canSignIn = true) {
  mocks.state = state
  return render(
    <CodexAccountsPane canSignIn={canSignIn} {...paneProps()} />,
  )
}

function meter(label: string) {
  return screen.getByRole('meter', { name: label })
}

function rowButton(email: string) {
  return screen.getByRole('button', { name: `Refresh usage for ${email}` }) as HTMLButtonElement
}

/** The first tbody row — index 0 is the header row. */
function bodyRow(index: number) {
  return screen.getAllByRole('row')[index + 1]
}

describe('CodexAccountsPane', () => {
  it('renders every account as its own row in the cockpit grid', () => {
    renderPane(stateAt({ accounts: [devAccount, samAccount], quotas: twoQuotas }))

    const region = screen.getByRole('region', { name: 'Codex accounts and usage' })
    expect(region).toBeTruthy()
    for (const header of ['Account', 'State', 'Plan', 'Resets', '5h window', 'Weekly window', 'Updated', 'Actions']) {
      expect(screen.getByRole('columnheader', { name: header })).toBeTruthy()
    }
    expect(screen.getAllByRole('row')).toHaveLength(3) // header + two accounts

    const devRow = bodyRow(0)
    expect(within(devRow).getByText('dev@example.com')).toBeTruthy()
    expect(within(devRow).getByText('Signed in')).toBeTruthy()
    expect(within(devRow).getByText('Pro')).toBeTruthy()
    expect(within(devRow).getByText('Device login')).toBeTruthy()
    expect(within(devRow).getByText('2')).toBeTruthy() // session resets, cockpit style
    const samRow = bodyRow(1)
    expect(within(samRow).getByText('sam@example.com')).toBeTruthy()
    expect(within(samRow).getByText('Plus')).toBeTruthy()
    expect(within(samRow).getByText('Browser login')).toBeTruthy()
    expect(within(samRow).getByText('5')).toBeTruthy()

    // Both windows on both rows: 78/23 primary, 41/64 weekly, plus stamps.
    // The meter labels name the window the endpoint reported (300 minutes
    // is "5h", a full week stays "Weekly"), not the column's constant slot.
    expect(screen.getByText('78% left')).toBeTruthy()
    expect(screen.getByText('23% left')).toBeTruthy()
    expect(screen.getAllByText(/^Resets /)).toHaveLength(4)
    expect(screen.getAllByText(/^Updated /)).toHaveLength(2)
    expect(screen.getAllByRole('meter')).toHaveLength(4)
    expect(meter('5h, 78% remaining').getAttribute('aria-valuenow')).toBe('78')
    expect(meter('5h, 23% remaining').getAttribute('aria-valuenow')).toBe('23')
    expect(meter('Weekly, 41% remaining').getAttribute('aria-valuenow')).toBe('41')
    expect(meter('Weekly, 64% remaining').getAttribute('aria-valuenow')).toBe('64')
  })

  it('counts a future reset down and carries the exact stamp as its title', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-01-01T12:00:00Z'))
    const now = Math.floor(Date.now() / 1000)
    renderPane(stateAt({
      accounts: [devAccount],
      quotas: {
        'acct-1': cardAt({
          quota: quotaAt({
            primary: { present: true, remainingPercent: 78, windowMinutes: 300, resetAt: now + 3_120 },
            secondary: { present: true, remainingPercent: 41, windowMinutes: 10_080, resetAt: now + 1_728_000 },
          }),
        }),
      },
    }))

    // 3120 s left in the 5h window, 1_728_000 s in the weekly one.
    expect(screen.getByText('Resets in 52m')).toBeTruthy()
    expect(screen.getByText('Resets in 20d 0h')).toBeTruthy()
    for (const line of screen.getAllByText(/^Resets in /)) {
      expect(line.getAttribute('title')).not.toBe('')
    }
  })

  it('shows the waking state on the fleet button and the affected row', () => {
    renderPane(stateAt({
      accounts: [devAccount, samAccount],
      quotas: twoQuotas,
      wakingIds: ['acct-2'],
      wakeError: 'The Codex accounts could not be woken. Try again.',
    }))

    expect((screen.getByRole('button', { name: 'Waking…' }) as HTMLButtonElement).disabled).toBe(true)
    expect(screen.queryByRole('button', { name: 'Wake all' })).toBeNull()
    expect(within(bodyRow(1)).getByText('Waking…')).toBeTruthy()
    expect(screen.getByRole('alert').textContent).toBe('The Codex accounts could not be woken. Try again.')
  })

  it('marks the sorted column for screen readers', () => {
    renderPane(stateAt({ accounts: [devAccount] }))

    expect(screen.getByRole('columnheader', { name: 'Account' }).getAttribute('aria-sort')).toBe('ascending')
    expect(screen.getByRole('columnheader', { name: 'State' }).getAttribute('aria-sort')).toBeNull()

    fireEvent.change(screen.getByRole('combobox', { name: 'Sort' }), { target: { value: 'state' } })
    expect(screen.getByRole('columnheader', { name: 'State' }).getAttribute('aria-sort')).toBe('ascending')
    expect(screen.getByRole('columnheader', { name: 'Account' }).getAttribute('aria-sort')).toBeNull()
  })

  it('sorts by resets descending and puts the emptiest last', () => {
    renderPane(stateAt({ accounts: [devAccount, samAccount, reauthAccount], quotas: twoQuotas }))

    // Default sort is account id: acct-1, acct-2. Resets sort must invert by count.
    fireEvent.change(screen.getByRole('combobox', { name: 'Sort' }), { target: { value: 'resets' } })
    expect(screen.getByRole('columnheader', { name: 'Resets' }).getAttribute('aria-sort')).toBe('descending')
    expect(bodyRow(0).textContent).toContain('sam@example.com') // 5 resets
    expect(bodyRow(1).textContent).toContain('dev@example.com') // 2 resets
    expect(within(bodyRow(2)).getByText('Sign-in needed')).toBeTruthy() // no count → last
  })

  it('narrows rows to the search needle and says so when nothing matches', () => {
    renderPane(stateAt({ accounts: [devAccount, samAccount], quotas: twoQuotas }))

    const search = screen.getByRole('searchbox', { name: 'Search accounts' })
    fireEvent.change(search, { target: { value: 'sam' } })
    expect(screen.queryByText('dev@example.com')).toBeNull()
    expect(screen.getByText('sam@example.com')).toBeTruthy()

    fireEvent.change(search, { target: { value: 'nothing@nowhere' } })
    expect(screen.getByText('No accounts match the current search and filters.')).toBeTruthy()
    expect((screen.getByRole('checkbox', { name: 'Select all accounts' }) as HTMLInputElement).checked).toBe(false)
  })

  it('searches by account id, not just email', () => {
    renderPane(stateAt({ accounts: [devAccount, samAccount], quotas: twoQuotas }))

    fireEvent.change(screen.getByRole('searchbox', { name: 'Search accounts' }), { target: { value: 'acct-2' } })
    expect(screen.queryByText('dev@example.com')).toBeNull()
    expect(screen.getByText('sam@example.com')).toBeTruthy()
  })

  it('filters rows by state and plan', () => {
    renderPane(stateAt({ accounts: [devAccount, samAccount, reauthSamAccount], quotas: twoQuotas }))

    fireEvent.change(screen.getByRole('combobox', { name: 'Plan' }), { target: { value: 'Plus' } })
    expect(screen.queryByText('dev@example.com')).toBeNull()
    expect(screen.getAllByRole('row')).toHaveLength(3) // header + sam + reauth-sam

    fireEvent.change(screen.getByRole('combobox', { name: 'State' }), { target: { value: 'reauth_needed' } })
    expect(screen.getAllByRole('row')).toHaveLength(2)
    expect(screen.getByText('sam@example.com')).toBeTruthy()

    fireEvent.change(screen.getByRole('combobox', { name: 'State' }), { target: { value: 'signed_out' } })
    expect(screen.getByText('No accounts match the current search and filters.')).toBeTruthy()
  })

  it('scopes the toolbar actions to the selection', () => {
    renderPane(stateAt({ accounts: [devAccount, samAccount], quotas: twoQuotas }))

    expect(screen.getByRole('button', { name: 'Wake all' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: /^Remove/ })).toBeNull()

    fireEvent.click(screen.getByRole('checkbox', { name: 'Select dev@example.com' }))
    expect(screen.getByRole('button', { name: 'Wake 1' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Check 1' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Remove 1' })).toBeTruthy()

    // Select-all flips in the remaining visible row and both checkboxes know.
    fireEvent.click(screen.getByRole('checkbox', { name: 'Select all accounts' }))
    expect(screen.getByRole('button', { name: 'Wake 2' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Remove 2' })).toBeTruthy()
    expect((screen.getByRole('checkbox', { name: 'Select sam@example.com' }) as HTMLInputElement).checked).toBe(true)

    fireEvent.click(screen.getByRole('button', { name: 'Clear selection' }))
    expect(screen.getByRole('button', { name: 'Wake all' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: /^Remove/ })).toBeNull()
  })

  it('wakes the fleet or just the selection', () => {
    renderPane(stateAt({ accounts: [devAccount, samAccount], quotas: twoQuotas }))

    fireEvent.click(screen.getByRole('button', { name: 'Wake all' }))
    expect(mocks.wake).toHaveBeenCalledWith(null)

    fireEvent.click(screen.getByRole('checkbox', { name: 'Select sam@example.com' }))
    fireEvent.click(screen.getByRole('button', { name: 'Wake 1' }))
    expect(mocks.wake).toHaveBeenCalledWith(['acct-2'])
  })

  it('checks all usage at once or just the signed-in part of the selection', () => {
    renderPane(stateAt({ accounts: [devAccount, reauthSamAccount], quotas: twoQuotas }))
    mocks.refreshQuota.mockClear() // drop the mount probes

    fireEvent.click(screen.getByRole('button', { name: 'Check usage' }))
    expect(mocks.refreshAll).toHaveBeenCalledTimes(1)
    expect(mocks.refreshQuota).not.toHaveBeenCalled()

    // Only the re-auth needed account is selected: nothing signed in to check.
    fireEvent.click(screen.getByRole('checkbox', { name: 'Select sam@example.com' }))
    expect((screen.getByRole('button', { name: 'Check 0' }) as HTMLButtonElement).disabled).toBe(true)

    fireEvent.click(screen.getByRole('checkbox', { name: 'Select dev@example.com' }))
    fireEvent.click(screen.getByRole('button', { name: 'Check 1' }))
    expect(mocks.refreshQuota).toHaveBeenCalledTimes(1)
    expect(mocks.refreshQuota).toHaveBeenCalledWith('acct-1')
    expect(mocks.refreshAll).toHaveBeenCalledTimes(1)
  })

  it('disables Check usage while the fleet check is already running', () => {
    renderPane(stateAt({ accounts: [devAccount], quotas: { 'acct-1': cardAt({ quota: devQuota }) }, probingAll: true }))

    expect((screen.getByRole('button', { name: 'Checking…' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('hands the selected accounts to the page for removal in row order', () => {
    renderPane(stateAt({ accounts: [devAccount, samAccount], quotas: twoQuotas }))

    fireEvent.click(screen.getByRole('checkbox', { name: 'Select sam@example.com' }))
    fireEvent.click(screen.getByRole('checkbox', { name: 'Select dev@example.com' }))
    fireEvent.click(screen.getByRole('button', { name: 'Remove 2' }))

    expect(mocks.onRemove).toHaveBeenCalledTimes(1)
    expect(mocks.onRemove).toHaveBeenCalledWith([devAccount, samAccount])
  })

  it('drops selections whose accounts disappeared', () => {
    const view = renderPane(stateAt({ accounts: [devAccount, samAccount], quotas: twoQuotas }))

    fireEvent.click(screen.getByRole('checkbox', { name: 'Select sam@example.com' }))
    expect(screen.getByRole('button', { name: 'Remove 1' })).toBeTruthy()

    mocks.state = stateAt({ accounts: [devAccount], quotas: twoQuotas })
    view.rerender(<CodexAccountsPane canSignIn {...paneProps()} />)

    expect(screen.getByRole('button', { name: 'Wake all' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Remove 1' })).toBeNull()
  })

  it('offers the compact list with the same meters and lifecycle', () => {
    renderPane(stateAt({ accounts: [devAccount, samAccount], quotas: twoQuotas }))

    fireEvent.click(screen.getByRole('button', { name: 'List' }))
    expect(screen.queryByRole('table')).toBeNull()
    expect(screen.getByRole('list', { name: 'Codex accounts and usage' })).toBeTruthy()
    expect(screen.getAllByRole('listitem')).toHaveLength(2)
    expect(screen.getAllByRole('meter')).toHaveLength(4)
    expect(screen.getByText('78% left')).toBeTruthy()
    expect(screen.getByText('Resets 5')).toBeTruthy() // resets count, compact style

    // The compact rows keep the per-row lifecycle and the selection.
    fireEvent.click(rowButton('sam@example.com'))
    expect(mocks.refreshQuota).toHaveBeenCalledWith('acct-2')
    fireEvent.click(screen.getByRole('checkbox', { name: 'Select sam@example.com' }))
    expect(screen.getByRole('button', { name: 'Remove 1' })).toBeTruthy()
  })

  it('probes each signed-in account once on mount', () => {
    renderPane(stateAt({ accounts: [devAccount, samAccount], quotas: twoQuotas }))

    expect(mocks.refreshQuota).not.toHaveBeenCalled()
  })

  it('probes signed-in accounts without settled cards', () => {
    renderPane(stateAt({ accounts: [devAccount], quotas: {} }))

    expect(mocks.refreshQuota).toHaveBeenCalledWith('acct-1')
  })

  it('skips accounts that are not signed in when probing', () => {
    renderPane(stateAt({ accounts: [reauthAccount, signedOutAccount], quotas: {} }))

    expect(mocks.refreshQuota).not.toHaveBeenCalled()
  })

  it('retries only the failed probe when the signed-in set changes', () => {
    const view = renderPane(stateAt({
      accounts: [devAccount, samAccount],
      quotas: {
        'acct-1': cardAt({ quota: devQuota }),
        'acct-2': cardAt({ error: 'quota fetch failed' }),
      },
    }))
    expect(mocks.refreshQuota).toHaveBeenCalledTimes(1)
    expect(mocks.refreshQuota).toHaveBeenCalledWith('acct-2')
    mocks.refreshQuota.mockClear()

    // Same membership, same outcome: no loop.
    mocks.state = stateAt({
      accounts: [devAccount, samAccount],
      quotas: {
        'acct-1': cardAt({ quota: devQuota }),
        'acct-2': cardAt({ error: 'quota fetch failed' }),
      },
    })
    view.rerender(<CodexAccountsPane canSignIn {...paneProps()} />)
    expect(mocks.refreshQuota).not.toHaveBeenCalled()
  })

  it('marks a pending row and keeps its last good windows', () => {
    renderPane(stateAt({
      accounts: [devAccount, samAccount],
      quotas: {
        'acct-1': cardAt({ quota: devQuota }),
        'acct-2': cardAt({ quota: samQuota, pending: true }),
      },
    }))

    expect(rowButton('dev@example.com').disabled).toBe(false)
    expect(rowButton('sam@example.com').disabled).toBe(true)
    expect(screen.getByText('78% left')).toBeTruthy()
    expect(screen.getByText('23% left')).toBeTruthy()
  })

  it('shows the state and hint a re-auth needed account carries', () => {
    renderPane(stateAt({ accounts: [reauthAccount], quotas: {} }))

    const row = bodyRow(0)
    expect(within(row).getByText('Sign-in needed')).toBeTruthy()
    expect(within(row).getByText('The session expired. Sign in again and the usage windows return.')).toBeTruthy()
    expect(within(row).queryByText(/^Resets in /)).toBeNull()
  })

  it('shows the state and hint a signed-out account carries', () => {
    renderPane(stateAt({ accounts: [signedOutAccount], quotas: {} }))

    const row = bodyRow(0)
    expect(within(row).getByText('Signed out')).toBeTruthy()
    expect(within(row).getByText('This account is signed out. Sign in again to bring it back.')).toBeTruthy()
  })

  it('shows a row error from a failed probe beside the last good windows', () => {
    renderPane(stateAt({
      accounts: [devAccount],
      quotas: { 'acct-1': cardAt({ quota: devQuota, error: 'quota fetch failed' }) },
    }))

    const row = bodyRow(0)
    expect(within(row).getByRole('alert').textContent).toBe('quota fetch failed')
    expect(within(row).getByText('78% left')).toBeTruthy()
  })

  it('offers row sign-in when the build can sign in and the session is missing', () => {
    renderPane(stateAt({ accounts: [reauthAccount], quotas: {} }))

    fireEvent.click(screen.getByRole('button', { name: 'Sign in again for dev@example.com' }))
    expect(mocks.onSignIn).toHaveBeenCalledTimes(1)
  })

  it('withholds row sign-in when the build cannot sign in', () => {
    renderPane(stateAt({ accounts: [reauthAccount], quotas: {} }), false)

    expect(screen.queryByRole('button', { name: 'Sign in again for dev@example.com' })).toBeNull()
  })

  it('asks the page before signing an account out', () => {
    renderPane(stateAt({ accounts: [devAccount], quotas: twoQuotas }))

    fireEvent.click(screen.getByRole('button', { name: 'Sign out dev@example.com' }))
    expect(mocks.onSignOut).toHaveBeenCalledTimes(1)
    expect(mocks.onSignOut).toHaveBeenCalledWith(devAccount)
  })

  it('keeps an updated stamp per settled row', () => {
    renderPane(stateAt({ accounts: [devAccount, samAccount], quotas: twoQuotas }))

    expect(screen.getAllByText(/^Updated /)).toHaveLength(2)
  })

  it('says a signed-out account is a blank slate, not a broken one', () => {
    renderPane(stateAt({ accounts: [signedOutAccount], quotas: {} }))

    const row = bodyRow(0)
    expect(within(row).queryByRole('alert')).toBeNull()
    expect(screen.queryByRole('meter')).toBeNull()
    // Plan keeps its badge; the resets count and both windows are blank.
    expect(within(row).getAllByText('—')).toHaveLength(3)
  })

  it('says usage is not loaded yet for a signed-in row without a probe', () => {
    renderPane(stateAt({ accounts: [devAccount], quotas: {} }))

    expect(screen.getByText('No usage loaded yet. Use Check usage.')).toBeTruthy()
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

    // The table keeps its grid with blank cells; the compact list spells it out.
    expect(screen.queryByRole('meter')).toBeNull()
    expect(within(bodyRow(0)).getAllByText('—')).toHaveLength(2)
    fireEvent.click(screen.getByRole('button', { name: 'List' }))
    expect(screen.getByText('No usage windows reported for this account.')).toBeTruthy()
  })

  it('offers the empty state with its single add-account action', () => {
    renderPane(stateAt({ accounts: [] }))

    expect(screen.getByText('No Codex accounts yet')).toBeTruthy()
    expect(screen.getByText('Add a ChatGPT account and its usage windows appear here.')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Add account' }))
    expect(mocks.onAddAccount).toHaveBeenCalledTimes(1)
  })

  it('offers no empty-state button when the build cannot sign in', () => {
    renderPane(stateAt({ accounts: [] }), false)

    expect(screen.getByText('Adding Codex accounts is not available in this build.')).toBeTruthy()
    expect(screen.queryByRole('button')).toBeNull()
  })
})
