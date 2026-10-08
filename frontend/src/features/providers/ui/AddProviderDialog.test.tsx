// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { CodexModelState } from '../../codex/application/codex-model'
import AddProviderDialog from './AddProviderDialog'

// Both the dialog shell and the real CodexConnectPane inside it read the same
// useCodex module, so one mock serves the whole codex step. The model mock only
// needs the methods this flow actually drives.
const mocks = vi.hoisted(() => ({
  state: null as unknown as CodexModelState,
  startLogin: vi.fn(),
  cancelLogin: vi.fn(),
  reopenAuthorizeUrl: vi.fn(),
  acknowledgeOutcome: vi.fn(),
}))
vi.mock('../../codex/ui/useCodex', () => ({
  useCodex: () => ({
    model: {
      startLogin: mocks.startLogin,
      cancelLogin: mocks.cancelLogin,
      reopenAuthorizeUrl: mocks.reopenAuthorizeUrl,
      acknowledgeOutcome: mocks.acknowledgeOutcome,
    },
    state: mocks.state,
  }),
}))

// Auto-cleanup needs vitest globals, which this project does not enable.
afterEach(cleanup)

beforeEach(() => {
  mocks.startLogin.mockReset()
  mocks.cancelLogin.mockReset()
  mocks.reopenAuthorizeUrl.mockReset()
  mocks.acknowledgeOutcome.mockReset()
  mocks.state = stateAt({})
})

function stateAt(overrides: Partial<CodexModelState>): CodexModelState {
  return {
    loginPhase: 'idle',
    loginError: '',
    account: { state: 'signed_out', email: '', plan: '', accountId: '', providerId: '' },
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

function renderDialog(step: 'choose' | 'codex', state: CodexModelState = stateAt({})) {
  const handlers = {
    onDismiss: vi.fn(),
    onBack: vi.fn(),
    onChoosePreset: vi.fn(),
    onCustom: vi.fn(),
    onSelectCodexProvider: vi.fn(),
    onDisconnect: vi.fn(),
  }
  mocks.state = state
  const view = render(
    <AddProviderDialog step={step} codexProviderId={null} {...handlers} />,
  )
  return { view, handlers }
}

describe('AddProviderDialog', () => {
  it('announces itself as the add provider dialog', () => {
    renderDialog('codex')

    expect(screen.getByRole('dialog', { name: 'Add provider' })).toBeTruthy()
    expect(screen.getByRole('heading', { level: 2, name: 'Add provider' })).toBeTruthy()
  })

  it('offers the preset and the custom path on the choose step', () => {
    renderDialog('choose')

    expect(screen.getByText('Start from a preset, or configure everything by hand.')).toBeTruthy()
    expect(screen.getByText('From list')).toBeTruthy()
    expect(screen.getByText('Curated presets that configure the endpoint for you. Currently one: Codex.')).toBeTruthy()
    expect(screen.getByText('Custom')).toBeTruthy()
    expect(screen.getByText('Enter the endpoint, limits and authentication yourself.')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Back' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Done' })).toBeNull()
  })

  it('routes to the preset flow when the list option is picked', () => {
    const { handlers } = renderDialog('choose')

    fireEvent.click(screen.getByText('From list'))
    expect(handlers.onChoosePreset).toHaveBeenCalledTimes(1)
    // Leaving the chooser consumes any outcome an earlier flow left showing.
    expect(mocks.acknowledgeOutcome).toHaveBeenCalledTimes(1)
  })

  it('routes to the custom editor when the custom option is picked', () => {
    const { handlers } = renderDialog('choose')

    fireEvent.click(screen.getByText('Custom'))
    expect(handlers.onCustom).toHaveBeenCalledTimes(1)
    expect(mocks.acknowledgeOutcome).toHaveBeenCalledTimes(1)
  })

  it('cancels a live login before switching to the preset flow', () => {
    const { handlers } = renderDialog('choose', stateAt({ loginPhase: 'waiting' }))

    fireEvent.click(screen.getByText('From list'))
    expect(mocks.cancelLogin).toHaveBeenCalledTimes(1)
    expect(mocks.acknowledgeOutcome).toHaveBeenCalledTimes(1)
    expect(handlers.onChoosePreset).toHaveBeenCalledTimes(1)
  })

  it('cancels a connecting login before switching to the custom editor', () => {
    const { handlers } = renderDialog('choose', stateAt({ loginPhase: 'connecting' }))

    fireEvent.click(screen.getByText('Custom'))
    expect(mocks.cancelLogin).toHaveBeenCalledTimes(1)
    expect(mocks.acknowledgeOutcome).toHaveBeenCalledTimes(1)
    expect(handlers.onCustom).toHaveBeenCalledTimes(1)
  })

  it('shows the codex step behind a back affordance', () => {
    renderDialog('codex')

    expect(screen.getByText('Presets configure the endpoint. The account is yours.')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Back' })).toBeTruthy()
    // The dialog embeds the real pane; its default method already offers the
    // browser sign-in, so the dialog footer itself carries no connect action.
    expect(screen.getByRole('button', { name: 'Sign in with ChatGPT' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Done' })).toBeNull()
  })

  it('cancels a live login when Back is used mid-wait', () => {
    const { handlers } = renderDialog('codex', stateAt({
      loginPhase: 'waiting',
      authorizeUrl: 'https://auth.openai.com/authorize',
    }))

    fireEvent.click(screen.getByRole('button', { name: 'Back' }))
    expect(mocks.cancelLogin).toHaveBeenCalledTimes(1)
    expect(handlers.onBack).toHaveBeenCalledTimes(1)
  })

  it('leaves an idle flow alone when Back is used', () => {
    const { handlers } = renderDialog('codex')

    fireEvent.click(screen.getByRole('button', { name: 'Back' }))
    expect(mocks.cancelLogin).not.toHaveBeenCalled()
    expect(handlers.onBack).toHaveBeenCalledTimes(1)
  })

  it('gates every exit while the account is being linked', () => {
    renderDialog('codex', stateAt({ loginPhase: 'exchanging' }))

    expect((screen.getByRole('button', { name: 'Close' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: 'Cancel' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: 'Back' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('swallows Escape while the account is being linked', () => {
    const { handlers } = renderDialog('codex', stateAt({ loginPhase: 'exchanging' }))

    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
    expect(handlers.onDismiss).not.toHaveBeenCalled()
    expect(mocks.cancelLogin).not.toHaveBeenCalled()
    expect(mocks.acknowledgeOutcome).not.toHaveBeenCalled()
  })

  it('cancels a live login when Escape is pressed mid-wait', () => {
    const { handlers } = renderDialog('codex', stateAt({ loginPhase: 'waiting' }))

    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
    expect(mocks.cancelLogin).toHaveBeenCalledTimes(1)
    expect(mocks.acknowledgeOutcome).toHaveBeenCalledTimes(1)
    expect(handlers.onDismiss).toHaveBeenCalledTimes(1)
  })

  it('explains a frozen chooser while the account is being linked', () => {
    renderDialog('choose', stateAt({ loginPhase: 'exchanging' }))

    // The codex step has the pane's status region for this; the chooser has
    // exactly one of its own, and it says why everything here is disabled.
    expect(screen.getByRole('status').textContent).toBe('Finishing sign-in…')
    expect((screen.getByText('From list').closest('button') as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByText('Custom').closest('button') as HTMLButtonElement).disabled).toBe(true)
  })

  it('offers Done only after a successful sign-in, and Done selects the new provider', () => {
    const { handlers } = renderDialog('codex', stateAt({
      loginPhase: 'success',
      account: { state: 'signed_in', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex' },
    }))

    // The status line the pane owns proves the success phase reached the user;
    // its email sits in a nested span, so the query reads the composed text of
    // that span rather than any single text node.
    expect(screen.getByText(
      (_, element) => element?.tagName === 'SPAN' && element.textContent === 'Signed in as dev@example.com',
    )).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Done' }))
    expect(mocks.acknowledgeOutcome).toHaveBeenCalledTimes(1)
    expect(handlers.onSelectCodexProvider).toHaveBeenCalledTimes(1)
  })

  it('cancels a live login and consumes the outcome when the dialog is dismissed', () => {
    const { handlers } = renderDialog('codex', stateAt({ loginPhase: 'waiting' }))

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(mocks.cancelLogin).toHaveBeenCalledTimes(1)
    expect(mocks.acknowledgeOutcome).toHaveBeenCalledTimes(1)
    expect(handlers.onDismiss).toHaveBeenCalledTimes(1)
  })

  it('consumes a terminal outcome on dismiss without touching the login', () => {
    const { handlers } = renderDialog('codex', stateAt({ loginPhase: 'success' }))

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(mocks.cancelLogin).not.toHaveBeenCalled()
    expect(mocks.acknowledgeOutcome).toHaveBeenCalledTimes(1)
    expect(handlers.onDismiss).toHaveBeenCalledTimes(1)
  })
})
