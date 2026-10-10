// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { CodexModelState } from '../../codex/application/codex-model'
import type { ProvidersModelState } from '../application/providers-model'
import type { Provider } from '../domain/provider'

vi.mock('./useProviders', () => ({ useProviders: vi.fn() }))
vi.mock('../../codex/ui/useCodex', () => ({ useCodex: vi.fn() }))
vi.mock('../../../app/services', () => ({ useAppServices: vi.fn() }))

import { useAppServices } from '../../../app/services'
import { useCodex } from '../../codex/ui/useCodex'
import { useProviders } from './useProviders'
import ProvidersPage from './ProvidersPage'

// Auto-cleanup needs vitest globals, which this project does not enable.
afterEach(cleanup)

const mockUseProviders = vi.mocked(useProviders)
const mockUseCodex = vi.mocked(useCodex)
const mockUseAppServices = vi.mocked(useAppServices)

const codexPreset: Provider = {
  id: 'codex',
  name: 'Codex — dev@example.com',
  baseUrl: 'https://chatgpt.com/backend-api/codex',
  authMode: 'bearer',
  authHeader: '',
  dialect: 'auto',
  modelsPath: '/models',
  format: 'responses',
  chatPath: '',
  imageCompat: false,
  rpm: 0,
  rateUnit: 'minute',
  cacheTtl: '',
  enabled: true,
  keyConfigured: false,
  keyCount: 0,
  builtin: false,
  preset: 'codex',
}

const customProvider: Provider = {
  id: 'lab',
  name: 'Lab relay',
  baseUrl: 'http://127.0.0.1:8080/v1',
  authMode: 'x-api-key',
  authHeader: '',
  dialect: 'auto',
  modelsPath: '/v1/models',
  format: 'auto',
  chatPath: '/v1/chat/completions',
  imageCompat: false,
  rpm: 0,
  rateUnit: 'minute',
  cacheTtl: '',
  enabled: true,
  keyConfigured: false,
  keyCount: 0,
  builtin: false,
}

const builtinProvider: Provider = {
  id: 'anthropic',
  name: 'Anthropic',
  baseUrl: 'https://api.anthropic.com',
  authMode: 'x-api-key',
  authHeader: '',
  dialect: 'auto',
  modelsPath: '/v1/models',
  format: 'auto',
  chatPath: '/v1/chat/completions',
  imageCompat: false,
  rpm: 0,
  rateUnit: 'minute',
  cacheTtl: '',
  enabled: true,
  keyConfigured: false,
  keyCount: 0,
  builtin: true,
}

const signedIn: CodexModelState = {
  loginPhase: 'idle',
  activeMethod: null,
  state: 'signed_in',
  accounts: [{ state: 'signed_in', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex' }],
  freshAccount: null,
  loginError: '',
  authorizeUrl: '',
  deviceUserCode: '',
  deviceVerificationUrl: '',
  importedFrom: '',
  quotas: {},
  logoutError: '',
}

// A success that nobody pressed Done for: the sticky phase right after a
// sign-in that finished outside the dialog.
const signedInSuccess: CodexModelState = { ...signedIn, loginPhase: 'success' }

function show(state: ProvidersModelState, codex: CodexModelState = signedIn, model: Record<string, unknown> = {}, codexModel: Record<string, unknown> = {}) {
  // Rendering reads state alone; the model methods sit behind handlers, so
  // only the tests that fire those handlers pass real stubs here.
  mockUseProviders.mockReturnValue({ model: model as never, state })
  mockUseCodex.mockReturnValue({ model: codexModel as never, state: codex })
  mockUseAppServices.mockReturnValue({ capabilities: ['codex.login'] } as never)
  return render(<ProvidersPage />)
}

beforeEach(() => {
  mockUseProviders.mockReset()
  mockUseCodex.mockReset()
  mockUseAppServices.mockReset()
})

describe('ProvidersPage inspector', () => {
  it('routes the codex preset delete through the account sign-in', async () => {
    const logout = vi.fn(async () => true)
    const refresh = vi.fn()
    show(
      {
        phase: 'ready',
        catalog: { activeId: 'lab', providers: [codexPreset, customProvider] },
        pendingId: '',
        error: '',
        health: new Map(),
      },
      signedIn,
      { clearError: vi.fn(), refresh },
      { logout },
    )

    fireEvent.click(screen.getByText('Codex — dev@example.com').closest('button') as HTMLElement)
    const preset = screen.getByRole('complementary', { name: 'Codex — dev@example.com provider details' })
    // The preset keeps the manual editor off (the sign-in owns its config),
    // but Delete is a deliberate cascade: it goes through the account with
    // remove, not through the generic providers.delete, which refuses presets.
    expect(within(preset).queryByRole('button', { name: 'Edit' })).toBeNull()
    fireEvent.click(within(preset).getByRole('button', { name: 'Delete' }))

    // The confirm names what the cascade removes before anything fires.
    const dialog = screen.getByRole('dialog', { name: 'Delete provider' })
    expect(within(dialog).getByText(/is signed out and the preset entry is deleted along with its model routes/)).toBeTruthy()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete provider' }))

    await waitFor(() => {
      // The preset delete is a deliberate cascade: every Codex account is
      // removed, which the multi-account logout spells out as (null, true).
      expect(logout).toHaveBeenCalledWith(null, true)
    })
    await waitFor(() => {
      expect(refresh).toHaveBeenCalled()
    })
  })

  it('deletes a custom provider together with its keys and routes', async () => {
    const del = vi.fn(async () => true)
    show(
      {
        phase: 'ready',
        catalog: { activeId: 'lab', providers: [{ ...customProvider, keyCount: 2 }, builtinProvider] },
        pendingId: '',
        error: '',
        health: new Map(),
      },
      signedIn,
      { clearError: vi.fn(), delete: del },
    )

    // The wide layout defaults the inspector to the active provider, which
    // here is the keyed custom one.
    const custom = screen.getByRole('complementary', { name: 'Lab relay provider details' })
    const deleteButton = within(custom).getByRole('button', { name: 'Delete' })
    // Keys ride the cascade, so the delete no longer parks behind a
    // "remove keys first" gate.
    expect(deleteButton.hasAttribute('disabled')).toBe(false)
    expect(within(custom).queryByText(/remove them in API Keys first/)).toBeNull()

    fireEvent.click(deleteButton)
    const dialog = screen.getByRole('dialog', { name: 'Delete provider' })
    expect(within(dialog).getByText(/2 API keys and the model routes that use this provider are deleted with it/)).toBeTruthy()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete provider' }))

    await waitFor(() => {
      expect(del).toHaveBeenCalledWith('lab')
    })
  })

  it('lets the active provider be deleted and says where the route moves', async () => {
    const del = vi.fn(async () => true)
    show(
      {
        phase: 'ready',
        catalog: { activeId: 'lab', providers: [customProvider, builtinProvider] },
        pendingId: '',
        error: '',
        health: new Map(),
      },
      signedIn,
      { clearError: vi.fn(), delete: del },
    )

    // The active provider keeps its delete: the cascade moves the route to
    // an enabled built-in in the same save, so nothing is left dangling.
    const custom = screen.getByRole('complementary', { name: 'Lab relay provider details' })
    const deleteButton = within(custom).getByRole('button', { name: 'Delete' })
    expect(deleteButton.hasAttribute('disabled')).toBe(false)
    expect(within(custom).queryByText('The active provider cannot be deleted')).toBeNull()

    fireEvent.click(deleteButton)
    const dialog = screen.getByRole('dialog', { name: 'Delete provider' })
    expect(within(dialog).getByText(/The active route moves to an enabled built-in provider/)).toBeTruthy()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete provider' }))

    await waitFor(() => {
      expect(del).toHaveBeenCalledWith('lab')
    })
  })

  it('keeps the generic edit off a codex preset row while custom and builtin providers keep it', () => {
    show({
      phase: 'ready',
      catalog: { activeId: 'lab', providers: [codexPreset, customProvider, builtinProvider] },
      pendingId: '',
      error: '',
      health: new Map(),
    })

    // The wide layout defaults the inspector to the active provider — the
    // custom one keeps its edit affordance.
    const custom = screen.getByRole('complementary', { name: 'Lab relay provider details' })
    expect(within(custom).getByRole('button', { name: 'Edit' })).toBeTruthy()

    // Builtin providers are curated configs, not account-managed presets,
    // so they stay editable.
    fireEvent.click(screen.getByText('Anthropic').closest('button') as HTMLElement)
    const builtin = screen.getByRole('complementary', { name: 'Anthropic provider details' })
    expect(within(builtin).getByRole('button', { name: 'Edit' })).toBeTruthy()

    // The codex preset is managed by the account sign-in, so the generic
    // editor must not offer a refused save path.
    fireEvent.click(screen.getByText('Codex — dev@example.com').closest('button') as HTMLElement)
    const preset = screen.getByRole('complementary', { name: 'Codex — dev@example.com provider details' })
    expect(within(preset).queryByRole('button', { name: 'Edit' })).toBeNull()
  })

  it('holds the neutral inspector instead of the active provider when the requested row is missing', () => {
    const refresh = vi.fn()
    const acknowledgeOutcome = vi.fn()
    show(
      {
        phase: 'ready',
        catalog: { activeId: 'lab', providers: [customProvider] },
        pendingId: '',
        error: '',
        health: new Map(),
      },
      signedInSuccess,
      { refresh },
      { acknowledgeOutcome },
    )

    // The sticky success already fired the one catalog refresh at mount.
    expect(refresh).toHaveBeenCalledTimes(1)

    // Wide layout: the default read is the active provider.
    expect(screen.getByRole('complementary', { name: 'Lab relay provider details' })).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Add provider' }))
    fireEvent.click(screen.getByText('From list').closest('button') as HTMLElement)
    fireEvent.click(screen.getByRole('button', { name: 'Done' }))

    // The catalog has no codex row yet, so the requested id stays
    // unresolved: neutral state, never a silent fallback to the active
    // provider. Done must not stack a second refresh on the success one.
    expect(screen.queryByRole('complementary', { name: 'Lab relay provider details' })).toBeNull()
    expect(screen.getByText('No provider selected')).toBeTruthy()
    expect(refresh).toHaveBeenCalledTimes(1)
  })
})

describe('ProvidersPage codex sign-in', () => {
  it('refreshes the providers catalog once when a sign-in succeeds without Done', () => {
    const refresh = vi.fn()
    const view = show({
      phase: 'ready',
      catalog: { activeId: 'lab', providers: [customProvider] },
      pendingId: '',
      error: '',
      health: new Map(),
    }, signedIn, { refresh })

    expect(refresh).not.toHaveBeenCalled()

    // The sign-in finishes outside the dialog (browser hand-off): the
    // sticky success phase reaches the page and the catalog refresh follows.
    mockUseCodex.mockReturnValue({ model: {} as never, state: signedInSuccess })
    view.rerender(<ProvidersPage />)
    expect(refresh).toHaveBeenCalledTimes(1)

    // Success is sticky — re-renders while it is unacknowledged never stack
    // another refresh.
    view.rerender(<ProvidersPage />)
    expect(refresh).toHaveBeenCalledTimes(1)
  })
})

describe('ProvidersPage codex disconnect', () => {
  it('renders the typed logout refusal in the disconnect dialog', () => {
    const refusal = 'Codex is the active provider. Switch the active route away from Codex before disconnecting.'
    show({
      phase: 'ready',
      catalog: { activeId: 'codex', providers: [codexPreset] },
      pendingId: '',
      error: '',
      health: new Map(),
    }, { ...signedIn, logoutError: refusal })

    // Wide layout defaults the inspector to the active codex preset; its
    // account section carries the disconnect.
    const preset = screen.getByRole('complementary', { name: 'Codex — dev@example.com provider details' })
    fireEvent.click(within(preset).getByRole('button', { name: 'Disconnect' }))

    // The refusal is a backend sentence written for the owner, so it
    // travels verbatim instead of a generic failure.
    expect(screen.getByRole('alert').textContent).toBe(refusal)
  })

  it('describes a disconnect as keeping the disabled entry', () => {
    show({
      phase: 'ready',
      catalog: { activeId: 'codex', providers: [codexPreset] },
      pendingId: '',
      error: '',
      health: new Map(),
    }, signedIn)

    const preset = screen.getByRole('complementary', { name: 'Codex — dev@example.com provider details' })
    fireEvent.click(within(preset).getByRole('button', { name: 'Disconnect' }))

    // A disconnect is the sign-out that keeps the entry: the dialog must
    // not promise the removal only the delete cascade performs.
    const dialog = screen.getByRole('dialog', { name: 'Disconnect Codex' })
    expect(within(dialog).getByText(/is signed out/)).toBeTruthy()
    expect(within(dialog).getByText(/The provider stays in the list, disabled/)).toBeTruthy()
    expect(within(dialog).queryByText(/provider is removed/)).toBeNull()
  })

  it('cuts an oversized logout error dump at a word boundary in the disconnect dialog', () => {
    const dump = `${'a'.repeat(199)} ${'b'.repeat(100)}`
    show({
      phase: 'ready',
      catalog: { activeId: 'codex', providers: [codexPreset] },
      pendingId: '',
      error: '',
      health: new Map(),
    }, { ...signedIn, logoutError: dump })

    const preset = screen.getByRole('complementary', { name: 'Codex — dev@example.com provider details' })
    fireEvent.click(within(preset).getByRole('button', { name: 'Disconnect' }))

    // A 200-character cut retreated to the last full word: the refusal-sized
    // sentences above are untouched, a raw dump cannot flood the dialog.
    expect(screen.getByRole('alert').textContent).toBe(`${'a'.repeat(199)}…`)
  })
})
