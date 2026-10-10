// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('./useApiKeys', () => ({ useApiKeys: vi.fn() }))
vi.mock('../../providers/ui/useProviders', () => ({ useProviders: vi.fn() }))
vi.mock('../../codex/ui/useCodex', () => ({ useCodex: vi.fn() }))
vi.mock('../../../app/services', () => ({ useAppServices: vi.fn() }))
// The pane's own behaviour is pinned in CodexAccountsPane.test.tsx; here it
// is a marker so the page test stays about the page's branching.
vi.mock('../../codex/ui/CodexAccountsPane', () => ({ default: () => <div data-testid="codex-accounts-pane" /> }))

import { useAppServices } from '../../../app/services'
import { useCodex } from '../../codex/ui/useCodex'
import { useProviders } from '../../providers/ui/useProviders'
import { useApiKeys } from './useApiKeys'
import ApiKeysPage from './ApiKeysPage'

// Auto-cleanup needs vitest globals, which this project does not enable.
afterEach(cleanup)

const mockUseApiKeys = vi.mocked(useApiKeys)
const mockUseProviders = vi.mocked(useProviders)
const mockUseCodex = vi.mocked(useCodex)
const mockUseAppServices = vi.mocked(useAppServices)
const mockLoad = vi.fn()

// A pool provider plus the signed-in codex preset the swap branch keys on.
const catalog = {
  providers: [
    { id: 'north-relay', name: 'North Relay', rateUnit: 'minute' },
    { id: 'codex', name: 'Codex', rateUnit: 'minute' },
  ],
  activeId: 'north-relay',
}

// The caption counts accounts, so the page has to read them from the codex
// snapshot instead of a hardcoded single-account sentence.
function codexAccount(accountId: string, email: string) {
  return { state: 'signed_in', email, plan: 'Pro', accountId, providerId: 'codex' }
}

function show(capabilities: readonly string[], accounts: readonly ReturnType<typeof codexAccount>[] = [codexAccount('acct-1', 'dev@example.com')]) {
  mockUseAppServices.mockReturnValue({ capabilities } as never)
  mockUseProviders.mockReturnValue({ state: { catalog } } as never)
  mockUseCodex.mockReturnValue({
    model: {} as never,
    state: {
      loginPhase: 'idle',
      activeMethod: null,
      state: accounts.length > 0 ? 'signed_in' : 'signed_out',
      accounts,
      freshAccount: null,
      loginError: '',
      authorizeUrl: '',
      deviceUserCode: '',
      deviceVerificationUrl: '',
      importedFrom: '',
      quotas: {},
      logoutError: '',
    },
  } as never)
  mockUseApiKeys.mockReturnValue({
    model: { load: mockLoad },
    state: { keys: [], phase: 'ready', checkingPool: false, error: '', poolReport: null, pendingId: null },
  } as never)
  return render(<ApiKeysPage />)
}

function selectProvider(id: string) {
  fireEvent.change(screen.getByRole('combobox'), { target: { value: id } })
}

beforeEach(() => {
  mockLoad.mockReset()
})

describe('ApiKeysPage codex accounts branch', () => {
  it('replaces the keys table with the accounts pane for the codex preset and never loads its keys', () => {
    show(['codex.login', 'codex.quota'])
    selectProvider('codex')

    expect(screen.getByTestId('codex-accounts-pane')).toBeTruthy()
    // The key-pool controls and the table itself must all be gone.
    expect(screen.queryByRole('table')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Check pool' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Bulk import' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Add key' })).toBeNull()
    // The key list is not fetched for a provider that has no keys.
    expect(mockLoad).not.toHaveBeenCalledWith('codex')
    expect(mockLoad).toHaveBeenCalledWith('north-relay')
    expect(screen.getByText('1 account · usage windows refresh on demand')).toBeTruthy()
    expect(screen.getByText('Sign-in lives on the Providers page')).toBeTruthy()
  })

  it('counts the signed-in accounts in the pane caption instead of a hardcoded one', () => {
    show(
      ['codex.login', 'codex.quota'],
      [codexAccount('acct-1', 'dev@example.com'), codexAccount('acct-2', 'sam@example.com')],
    )
    selectProvider('codex')

    expect(screen.getByTestId('codex-accounts-pane')).toBeTruthy()
    expect(screen.getByText('2 accounts · usage windows refresh on demand')).toBeTruthy()
    expect(screen.queryByText('1 account · usage windows refresh on demand')).toBeNull()
  })

  it('keeps the keys table when the handshake never promised codex.quota', () => {
    show(['codex.login'])
    selectProvider('codex')

    expect(screen.queryByTestId('codex-accounts-pane')).toBeNull()
    expect(screen.getByRole('table')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Add key' })).toBeTruthy()
    // The pane is gated on the capability, so the pool provider loads.
    expect(mockLoad).toHaveBeenCalledWith('codex')
    expect(screen.getByText('0 keys · lower number means higher priority')).toBeTruthy()
  })

  it('keeps the keys table for pool providers even when the codex capability is present', () => {
    show(['codex.login', 'codex.quota'])

    expect(screen.queryByTestId('codex-accounts-pane')).toBeNull()
    expect(screen.getByRole('table')).toBeTruthy()
    expect(screen.getByText('No keys configured for this provider.')).toBeTruthy()
    expect(screen.getByText('Secrets and proxy credentials are write-only')).toBeTruthy()
  })
})
