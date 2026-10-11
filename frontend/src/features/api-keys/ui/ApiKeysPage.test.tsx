// @vitest-environment jsdom

import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('./useApiKeys', () => ({ useApiKeys: vi.fn() }))
vi.mock('../../providers/ui/useProviders', () => ({ useProviders: vi.fn() }))
vi.mock('../../codex/ui/useCodex', () => ({ useCodex: vi.fn() }))
vi.mock('../../../app/services', () => ({ useAppServices: vi.fn() }))
// The pane's own behaviour is pinned in CodexAccountsPane.test.tsx; here it
// is a harness that fires the callbacks, so the page test stays about the
// page's branching and wiring.
vi.mock('../../codex/ui/CodexAccountsPane', () => ({
  default: (props: PaneProps) => (
    <div data-testid="codex-accounts-pane" data-can-sign-in={String(props.canSignIn)}>
      <button type="button" onClick={props.onAddAccount}>pane-add</button>
      <button type="button" onClick={props.onSignIn}>pane-sign-in</button>
      <button type="button" onClick={() => props.onSignOut({ accountId: 'acct-1', email: 'dev@example.com', state: 'signed_in' })}>pane-sign-out</button>
      <button type="button" onClick={() => props.onRemove([
        { accountId: 'acct-1', email: 'dev@example.com', state: 'signed_in' },
        { accountId: 'acct-2', email: 'sam@example.com', state: 'signed_in' },
      ])}>pane-remove</button>
    </div>
  ),
}))
vi.mock('../../codex/ui/CodexSignInDialog', () => ({
  default: (props: SignInDialogProps) => (
    <div data-testid="codex-sign-in-dialog">
      <button type="button" onClick={props.onDismiss}>mock-dismiss</button>
      <button type="button" onClick={() => props.onDisconnect('acct-1')}>mock-disconnect</button>
    </div>
  ),
}))
vi.mock('../../codex/ui/DisconnectCodexDialog', () => ({
  default: (props: DisconnectDialogProps) => (
    <div data-testid="codex-disconnect-dialog">
      {props.accounts.map((account) => <span key={account.accountId}>{account.email}</span>)}
      <span>{props.remove ? 'remove-mode' : 'disconnect-mode'}</span>
      <button type="button" onClick={props.onCancel}>mock-cancel</button>
      <button type="button" onClick={() => void props.onConfirm()}>mock-confirm</button>
    </div>
  ),
}))

import { useAppServices } from '../../../app/services'
import { useCodex } from '../../codex/ui/useCodex'
import { useProviders } from '../../providers/ui/useProviders'
import { useApiKeys } from './useApiKeys'
import ApiKeysPage from './ApiKeysPage'

// Auto-cleanup needs vitest globals, which this project does not enable.
afterEach(cleanup)

type PaneProps = {
  canSignIn: boolean
  onAddAccount: () => void
  onSignIn: () => void
  onSignOut: (account: { accountId: string; email: string; state: string }) => void
  onRemove: (accounts: readonly { accountId: string; email: string; state: string }[]) => void
}
type SignInDialogProps = {
  onDismiss: () => void
  onDisconnect: (accountId: string) => void
}
type DisconnectDialogProps = {
  accounts: readonly { accountId: string; email: string; state: string }[]
  remove: boolean
  pending: boolean
  error: string
  onCancel: () => void
  onConfirm: () => Promise<void>
}

const mockUseApiKeys = vi.mocked(useApiKeys)
const mockUseProviders = vi.mocked(useProviders)
const mockUseCodex = vi.mocked(useCodex)
const mockUseAppServices = vi.mocked(useAppServices)
const mockLoad = vi.fn()
const mockStartLogin = vi.fn()
const mockLogout = vi.fn()

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

function show(
  capabilities: readonly string[],
  accounts: readonly ReturnType<typeof codexAccount>[] = [codexAccount('acct-1', 'dev@example.com')],
  codexStateExtra: Record<string, unknown> = {},
) {
  mockUseAppServices.mockReturnValue({ capabilities } as never)
  mockUseProviders.mockReturnValue({ state: { catalog } } as never)
  mockUseCodex.mockReturnValue({
    model: { startLogin: mockStartLogin, logout: mockLogout },
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
      ...codexStateExtra,
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
  mockStartLogin.mockReset()
  mockLogout.mockReset()
  mockLogout.mockResolvedValue(true)
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
    expect(screen.getByText('Accounts sign in and out on this page')).toBeTruthy()
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

  it('offers the codex header action and passes the sign-in capability down to the pane', () => {
    show(['codex.login', 'codex.quota'])
    selectProvider('codex')

    // Usage refresh lives in the pane next to the accounts it refreshes, so
    // the header keeps only the account lifecycle action.
    expect(screen.queryByRole('button', { name: 'Refresh usage' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Add account' })).toBeTruthy()
    expect(screen.getByTestId('codex-accounts-pane').getAttribute('data-can-sign-in')).toBe('true')
  })

  it('hides the add-account action when the build cannot sign in', () => {
    show(['codex.quota'])
    selectProvider('codex')

    expect(screen.queryByRole('button', { name: 'Add account' })).toBeNull()
    expect(screen.getByTestId('codex-accounts-pane').getAttribute('data-can-sign-in')).toBe('false')
  })

  it('opens the sign-in dialog from the header and closes it on dismiss', () => {
    show(['codex.login', 'codex.quota'])
    selectProvider('codex')

    fireEvent.click(screen.getByRole('button', { name: 'Add account' }))
    expect(screen.getByTestId('codex-sign-in-dialog')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'mock-dismiss' }))
    expect(screen.queryByTestId('codex-sign-in-dialog')).toBeNull()
  })

  it('starts the login when a row asks to renew its session', () => {
    show(['codex.login', 'codex.quota'])
    selectProvider('codex')

    fireEvent.click(screen.getByRole('button', { name: 'pane-sign-in' }))

    expect(mockStartLogin).toHaveBeenCalledTimes(1)
    expect(screen.getByTestId('codex-sign-in-dialog')).toBeTruthy()
  })

  it('opens the disconnect dialog for the row the pane asked to sign out', () => {
    show(
      ['codex.login', 'codex.quota'],
      [codexAccount('acct-1', 'dev@example.com'), codexAccount('acct-2', 'sam@example.com')],
    )
    selectProvider('codex')

    fireEvent.click(screen.getByRole('button', { name: 'pane-sign-out' }))

    const dialog = screen.getByTestId('codex-disconnect-dialog')
    expect(dialog.textContent).toContain('dev@example.com')
    expect(dialog.textContent).toContain('disconnect-mode')
  })

  it('signs the account out when the disconnect is confirmed', async () => {
    show(
      ['codex.login', 'codex.quota'],
      [codexAccount('acct-1', 'dev@example.com'), codexAccount('acct-2', 'sam@example.com')],
    )
    selectProvider('codex')
    fireEvent.click(screen.getByRole('button', { name: 'pane-sign-out' }))

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'mock-confirm' }))
    })

    expect(mockLogout).toHaveBeenCalledWith('acct-1')
    expect(screen.queryByTestId('codex-disconnect-dialog')).toBeNull()
  })

  it('keeps the disconnect dialog open when the sign-out is refused', async () => {
    mockLogout.mockResolvedValue(false)
    show(
      ['codex.login', 'codex.quota'],
      [codexAccount('acct-1', 'dev@example.com'), codexAccount('acct-2', 'sam@example.com')],
    )
    selectProvider('codex')
    fireEvent.click(screen.getByRole('button', { name: 'pane-sign-out' }))

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'mock-confirm' }))
    })

    expect(mockLogout).toHaveBeenCalledWith('acct-1')
    expect(screen.getByTestId('codex-disconnect-dialog')).toBeTruthy()
  })

  it('confirms a batch removal that covers every account with one fleet command', async () => {
    show(
      ['codex.login', 'codex.quota'],
      [codexAccount('acct-1', 'dev@example.com'), codexAccount('acct-2', 'sam@example.com')],
    )
    selectProvider('codex')
    fireEvent.click(screen.getByRole('button', { name: 'pane-remove' }))

    const dialog = screen.getByTestId('codex-disconnect-dialog')
    expect(dialog.textContent).toContain('dev@example.com')
    expect(dialog.textContent).toContain('sam@example.com')
    expect(dialog.textContent).toContain('remove-mode')

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'mock-confirm' }))
    })

    expect(mockLogout).toHaveBeenCalledTimes(1)
    expect(mockLogout).toHaveBeenCalledWith(null, true)
    expect(screen.queryByTestId('codex-disconnect-dialog')).toBeNull()
  })

  it('removes a partial batch one account at a time', async () => {
    show(
      ['codex.login', 'codex.quota'],
      [codexAccount('acct-1', 'dev@example.com'), codexAccount('acct-2', 'sam@example.com'), codexAccount('acct-3', 'third@example.com')],
    )
    selectProvider('codex')
    fireEvent.click(screen.getByRole('button', { name: 'pane-remove' }))

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'mock-confirm' }))
    })

    expect(mockLogout).toHaveBeenCalledWith('acct-1', true)
    expect(mockLogout).toHaveBeenCalledWith('acct-2', true)
    expect(mockLogout).not.toHaveBeenCalledWith(null, true)
    expect(screen.queryByTestId('codex-disconnect-dialog')).toBeNull()
  })

  it('keeps the removal dialog open when the batch is refused', async () => {
    mockLogout.mockResolvedValue(false)
    show(
      ['codex.login', 'codex.quota'],
      [codexAccount('acct-1', 'dev@example.com'), codexAccount('acct-2', 'sam@example.com')],
    )
    selectProvider('codex')
    fireEvent.click(screen.getByRole('button', { name: 'pane-remove' }))

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'mock-confirm' }))
    })

    expect(mockLogout).toHaveBeenCalledTimes(1)
    expect(mockLogout).toHaveBeenCalledWith(null, true)
    expect(screen.getByTestId('codex-disconnect-dialog')).toBeTruthy()
  })

  it('disconnects from inside the sign-in dialog through the same page handler', () => {
    show(['codex.login', 'codex.quota'])
    selectProvider('codex')
    fireEvent.click(screen.getByRole('button', { name: 'Add account' }))

    fireEvent.click(screen.getByRole('button', { name: 'mock-disconnect' }))

    const dialog = screen.getByTestId('codex-disconnect-dialog')
    expect(dialog.textContent).toContain('dev@example.com')
    // The sign-in flow stays open underneath, like the Providers page stack.
    expect(screen.getByTestId('codex-sign-in-dialog')).toBeTruthy()
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
