// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { CodexAccount } from '../domain/codex'
import DisconnectCodexDialog from './DisconnectCodexDialog'

// The dialog reads the shared account set itself to learn which signed-in
// accounts survive the batch; the props carry only the action — which
// accounts, and whether they also leave the device. The mock stands in for
// that state exactly as the pages hand it over.
const mocks = vi.hoisted(() => ({
  state: { accounts: [] as CodexAccount[] },
}))
vi.mock('./useCodex', () => ({
  useCodex: () => ({ state: mocks.state }),
}))

// Auto-cleanup needs vitest globals, which this project does not enable.
afterEach(cleanup)

beforeEach(() => {
  mocks.state = { accounts: [] }
})

function accountAt(overrides: Partial<CodexAccount>): CodexAccount {
  return { state: 'signed_in', email: '', plan: '', accountId: '', providerId: '', ...overrides }
}

const dev = (overrides: Partial<CodexAccount> = {}) => accountAt({ accountId: 'acct-1', email: 'dev@example.com', plan: 'Pro', ...overrides })
const sam = (overrides: Partial<CodexAccount> = {}) => accountAt({ accountId: 'acct-2', email: 'sam@example.com', plan: 'Plus', ...overrides })

function renderDialog(accounts: readonly CodexAccount[], remove = false, overrides: { pending?: boolean; error?: string; onCancel?: () => void; onConfirm?: () => void } = {}) {
  return render(
    <DisconnectCodexDialog
      accounts={accounts}
      remove={remove}
      pending={overrides.pending ?? false}
      error={overrides.error ?? ''}
      onCancel={overrides.onCancel ?? vi.fn()}
      onConfirm={overrides.onConfirm ?? vi.fn()}
    />,
  )
}

// The copy paragraph is one node carrying the whole sentence: who, what
// happens, and what remains of the provider. Asserting its text end to end
// keeps the three sentences from drifting apart.
function dialogCopy() {
  return screen.getByRole('dialog').querySelector('header p')?.textContent ?? ''
}

describe('DisconnectCodexDialog', () => {
  it('names the one account it signs out and keeps the provider alive while others remain', () => {
    mocks.state = { accounts: [dev(), sam()] }
    renderDialog([dev()])

    expect(screen.getByRole('dialog', { name: 'Disconnect Codex' })).toBeTruthy()
    expect(screen.getByText('Disconnect Codex?')).toBeTruthy()
    expect(dialogCopy()).toBe(
      'dev@example.com is signed out. The provider stays in the list and keeps working through its other signed-in accounts.',
    )
    expect(screen.getByRole('button', { name: 'Disconnect' })).toBeTruthy()
    expect(screen.queryByRole('listitem')).toBe(null)
  })

  it('says the provider is disabled when the last signed-in account goes', () => {
    mocks.state = { accounts: [dev(), accountAt({ state: 'signed_out', accountId: 'acct-3', email: 'old@example.com' })] }
    renderDialog([dev()])

    // Signed-out accounts are not survivors: only a left-over signed-in
    // account keeps the preset routing.
    expect(dialogCopy()).toBe(
      'dev@example.com is signed out. The provider stays in the list, disabled, and model routes that used it must be reassigned. Connect again at any time.',
    )
  })

  it('falls back to neutral wording for an account without an email', () => {
    renderDialog([accountAt({ accountId: 'acct-9' })])

    expect(dialogCopy()).toBe(
      'The account is signed out. The provider stays in the list, disabled, and model routes that used it must be reassigned. Connect again at any time.',
    )
  })

  it('states that a removal also drops the account from this device', () => {
    renderDialog([dev()], true)

    expect(screen.getByRole('dialog', { name: 'Remove Codex account' })).toBeTruthy()
    expect(screen.getByText('Remove Codex account?')).toBeTruthy()
    expect(dialogCopy()).toBe(
      'dev@example.com is signed out and removed from this device. The provider stays in the list, disabled, and model routes that used it must be reassigned. Add the account again at any time.',
    )
    expect(screen.getByRole('button', { name: 'Remove' })).toBeTruthy()
  })

  it('lists every account a batch touches and does not count the batch itself as a survivor', () => {
    const accounts = [dev(), sam()]
    mocks.state = { accounts }
    renderDialog(accounts, true)

    expect(screen.getByRole('dialog', { name: 'Remove Codex accounts' })).toBeTruthy()
    expect(dialogCopy()).toBe(
      '2 accounts are signed out and removed from this device. The provider stays in the list, disabled, and model routes that used it must be reassigned. Add the accounts again at any time.',
    )
    const items = screen.getAllByRole('listitem')
    expect(items.map((item) => item.textContent)).toEqual(['dev@example.com', 'sam@example.com'])
    expect(screen.getByRole('button', { name: 'Remove' })).toBeTruthy()
  })

  it('keeps the provider alive in batch copy when a signed-in account survives it', () => {
    mocks.state = { accounts: [dev(), sam(), accountAt({ accountId: 'acct-3', email: 'third@example.com' })] }
    renderDialog([dev(), sam()], true)

    expect(dialogCopy()).toBe(
      '2 accounts are signed out and removed from this device. The provider stays in the list and keeps working through its other signed-in accounts.',
    )
    expect(screen.getAllByRole('listitem').map((item) => item.textContent)).toEqual(['dev@example.com', 'sam@example.com'])
  })

  it('keeps the plural disconnect copy', () => {
    mocks.state = { accounts: [dev(), sam(), accountAt({ accountId: 'acct-3', email: 'third@example.com' })] }
    renderDialog([dev(), sam()])

    expect(screen.getByRole('dialog', { name: 'Disconnect Codex' })).toBeTruthy()
    expect(dialogCopy()).toBe('2 accounts are signed out. The provider stays in the list and keeps working through its other signed-in accounts.')
    expect(screen.getAllByRole('listitem').map((item) => item.textContent)).toEqual(['dev@example.com', 'sam@example.com'])
    expect(screen.getByRole('button', { name: 'Disconnect' })).toBeTruthy()
  })

  it('lists the account id when a batched account has no email', () => {
    mocks.state = { accounts: [dev(), accountAt({ accountId: 'acct-7' })] }
    renderDialog([dev(), accountAt({ accountId: 'acct-7' })], true)

    expect(screen.getAllByRole('listitem').map((item) => item.textContent)).toEqual(['dev@example.com', 'acct-7'])
  })

  it('blocks both buttons while an operation is pending', () => {
    renderDialog([dev()], true, { pending: true })

    expect((screen.getByRole('button', { name: 'Removing…' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: 'Cancel' }) as HTMLButtonElement).disabled).toBe(true)

    cleanup()
    renderDialog([dev()], false, { pending: true })
    expect((screen.getByRole('button', { name: 'Disconnecting…' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('surfaces a failed attempt without leaving the dialog', () => {
    renderDialog([dev()], true, { error: 'The account could not be removed.' })

    expect(screen.getByRole('alert').textContent).toBe('The account could not be removed.')
  })

  it('runs the confirm and cancel callbacks', () => {
    const onConfirm = vi.fn()
    const onCancel = vi.fn()
    renderDialog([dev()], true, { onConfirm, onCancel })

    fireEvent.click(screen.getByRole('button', { name: 'Remove' }))
    expect(onConfirm).toHaveBeenCalledTimes(1)

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onCancel).toHaveBeenCalledTimes(1)
  })

  it('renders nothing without accounts', () => {
    renderDialog([], true)

    expect(screen.queryByRole('dialog')).toBe(null)
  })
})
