// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { CodexAccount, CodexQuotaReport } from '../domain/codex'
import type { CodexModelState } from '../application/codex-model'
import CodexAccountsPane from './CodexAccountsPane'

// The pane reads the shared Codex state through useCodex and owns exactly one
// action — the usage probe — so the mock stays that small.
const mocks = vi.hoisted(() => ({
  state: null as unknown as CodexModelState,
  refreshQuota: vi.fn(),
}))
vi.mock('./useCodex', () => ({
  useCodex: () => ({
    model: { refreshQuota: mocks.refreshQuota },
    state: mocks.state,
  }),
}))

// Auto-cleanup needs vitest globals, which this project does not enable.
afterEach(cleanup)

beforeEach(() => {
  mocks.refreshQuota.mockReset()
  mocks.refreshQuota.mockResolvedValue(undefined)
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

function stateAt(overrides: Partial<CodexModelState>): CodexModelState {
  return {
    loginPhase: 'idle',
    loginError: '',
    account: accountAt({}),
    authorizeUrl: '',
    activeMethod: null,
    deviceUserCode: '',
    deviceVerificationUrl: '',
    importedFrom: '',
    logoutError: '',
    quota: null,
    quotaPending: false,
    quotaError: '',
    ...overrides,
  }
}

const signedInAccount = accountAt({ state: 'signed_in', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex' })

function renderPane(state: CodexModelState) {
  mocks.state = state
  return render(<CodexAccountsPane />)
}

function meter(label: string | RegExp) {
  return screen.getByRole('meter', { name: label }) as HTMLElement
}

describe('CodexAccountsPane', () => {
  it('renders the signed-in account with both usage windows and probes once on mount', () => {
    renderPane(stateAt({ account: signedInAccount, quota: quotaAt({}) }))

    expect(screen.getByText('dev@example.com')).toBeTruthy()
    expect(screen.getByText('Pro')).toBeTruthy()
    expect(screen.getByText('Signed in')).toBeTruthy()
    expect(meter(/5h window/).getAttribute('aria-valuenow')).toBe('78')
    expect(meter(/5h window/).getAttribute('aria-valuetext')).toBe('78% remaining')
    expect(meter(/Weekly window/).getAttribute('aria-valuenow')).toBe('41')
    expect(screen.getByText('78% left')).toBeTruthy()
    expect(screen.getByText('41% left')).toBeTruthy()
    expect(screen.getAllByText(/^Resets /)).toHaveLength(2)
    expect(screen.getByText(/^Updated /)).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Refresh usage' })).toBeTruthy()
    // Mount with a live session triggers exactly one probe; nothing polls.
    expect(mocks.refreshQuota).toHaveBeenCalledTimes(1)
  })

  it('probes again when Refresh usage is clicked, and only then', () => {
    renderPane(stateAt({ account: signedInAccount, quota: quotaAt({}) }))
    fireEvent.click(screen.getByRole('button', { name: 'Refresh usage' }))
    expect(mocks.refreshQuota).toHaveBeenCalledTimes(2)
  })

  it('keeps the last good windows beside a failed probe\'s alert', () => {
    renderPane(stateAt({
      account: signedInAccount,
      quota: quotaAt({ secondary: { present: false, remainingPercent: 100 } }),
      quotaError: 'The Codex usage could not be loaded.',
    }))

    expect(screen.getByRole('alert').textContent).toBe('The Codex usage could not be loaded.')
    expect(meter(/5h window/).getAttribute('aria-valuenow')).toBe('78')
    // The unreported window renders no row at all.
    expect(screen.queryByRole('meter', { name: /Weekly window/ })).toBeNull()
  })

  it('shows the checking state on the button while a probe is in flight', () => {
    renderPane(stateAt({ account: signedInAccount, quotaPending: true }))

    const button = screen.getByRole('button', { name: /Checking…/ }) as HTMLButtonElement
    expect(button.disabled).toBe(true)
    expect(screen.getByText('Checking usage…')).toBeTruthy()
  })

  it('answers a reauth session with the expiry hint, no meters, no alert and a locked button', () => {
    renderPane(stateAt({
      account: accountAt({ state: 'reauth_needed', email: 'dev@example.com' }),
      quotaError: 'The account rejected the usage request. Sign in again from the Providers page.',
    }))

    expect(screen.getByText('Sign-in needed')).toBeTruthy()
    expect(screen.getByText('The session expired. Sign in again from the Providers page and the usage windows return.')).toBeTruthy()
    expect(screen.queryByRole('meter')).toBeNull()
    expect(screen.queryByRole('alert')).toBeNull()
    const button = screen.getByRole('button', { name: 'Refresh usage' }) as HTMLButtonElement
    expect(button.disabled).toBe(true)
    // No live session, so mount did not probe.
    expect(mocks.refreshQuota).not.toHaveBeenCalled()
  })

  it('answers a signed-out session with the sign-in hint and no meters', () => {
    renderPane(stateAt({}))

    expect(screen.getByText('No Codex account')).toBeTruthy()
    expect(screen.getByText('Signed out')).toBeTruthy()
    expect(screen.getByText('Codex is not signed in. Sign in from the Providers page and the usage windows appear here.')).toBeTruthy()
    expect(screen.queryByRole('meter')).toBeNull()
    expect(mocks.refreshQuota).not.toHaveBeenCalled()
  })
})
