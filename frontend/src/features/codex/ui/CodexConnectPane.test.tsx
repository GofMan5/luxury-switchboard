// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { CodexAccount } from '../domain/codex'
import type { CodexModelState } from '../application/codex-model'
import CodexConnectPane from './CodexConnectPane'

// The pane reads the shared Codex state through useCodex; the model itself is
// only touched for the actions the pane owns, so the mock stays that small.
const mocks = vi.hoisted(() => ({
  state: null as unknown as CodexModelState,
  startLogin: vi.fn(),
  startDeviceLogin: vi.fn(),
  importFromJson: vi.fn(),
  importFromFiles: vi.fn(),
  reopenAuthorizeUrl: vi.fn(),
  openVerificationUrl: vi.fn(),
  acknowledgeOutcome: vi.fn(),
  pickAuthFiles: vi.fn(),
}))
vi.mock('./useCodex', () => ({
  useCodex: () => ({
    model: {
      startLogin: mocks.startLogin,
      startDeviceLogin: mocks.startDeviceLogin,
      importFromJson: mocks.importFromJson,
      importFromFiles: mocks.importFromFiles,
      reopenAuthorizeUrl: mocks.reopenAuthorizeUrl,
      openVerificationUrl: mocks.openVerificationUrl,
      acknowledgeOutcome: mocks.acknowledgeOutcome,
    },
    state: mocks.state,
  }),
}))
// The native picker only exists inside the desktop shell; the tests decide
// whether the pane gets paths, a cancel, or no shell at all.
vi.mock('../../../platform/lifecycle/pick-auth-files', () => ({
  pickAuthFiles: mocks.pickAuthFiles,
}))

// Auto-cleanup needs vitest globals, which this project does not enable.
afterEach(cleanup)

beforeEach(() => {
  mocks.startLogin.mockReset()
  mocks.startDeviceLogin.mockReset()
  mocks.importFromJson.mockReset()
  mocks.importFromFiles.mockReset()
  mocks.reopenAuthorizeUrl.mockReset()
  mocks.openVerificationUrl.mockReset()
  mocks.acknowledgeOutcome.mockReset()
  mocks.pickAuthFiles.mockReset()
  mocks.pickAuthFiles.mockResolvedValue(null)
  mocks.state = stateAt({})
})

function accountAt(overrides: Partial<CodexAccount>): CodexAccount {
  return { state: 'signed_out', email: '', plan: '', accountId: '', providerId: '', ...overrides }
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

function renderPane(state: CodexModelState, providerExists = false) {
  mocks.state = state
  return render(
    <CodexConnectPane
      providerExists={providerExists}
      onSelectCodexRow={vi.fn()}
      onDisconnect={vi.fn()}
    />,
  )
}

// The success line nests the email inside its own span, so no single text
// node carries the whole sentence: the matcher reads the composed text of the
// line's own span (the wrapping phase div carries the same text as an ancestor).
function successLine(email: string) {
  return screen.getByText(
    (_, element) => element?.tagName === 'SPAN' && element.textContent === `Signed in as ${email}`,
  )
}

function radio(name: RegExp) {
  return screen.getByRole('radio', { name }) as HTMLButtonElement
}

describe('CodexConnectPane', () => {
  it('offers the four sign-in ways on the connect card when no provider is linked yet', () => {
    renderPane(stateAt({}))

    expect(screen.getByText('Uses the ChatGPT account you sign in with. Endpoint, dialect and limits come from the preset.')).toBeTruthy()
    expect(screen.getAllByRole('radio')).toHaveLength(4)
    expect(radio(/Official sign-in/).getAttribute('aria-checked')).toBe('true')
    expect(radio(/Device code/).getAttribute('aria-checked')).toBe('false')
    expect(radio(/Import JSON/).getAttribute('aria-checked')).toBe('false')
    expect(radio(/Import from file/).getAttribute('aria-checked')).toBe('false')
    // The default pick is the browser flow, so its body shows a start button.
    expect(screen.getByRole('button', { name: 'Sign in with ChatGPT' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'View in list' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Disconnect' })).toBeNull()
  })

  it('starts the browser login when Sign in with ChatGPT is clicked', () => {
    renderPane(stateAt({}))

    fireEvent.click(screen.getByRole('button', { name: 'Sign in with ChatGPT' }))
    expect(mocks.startLogin).toHaveBeenCalledTimes(1)
    expect(mocks.reopenAuthorizeUrl).not.toHaveBeenCalled()
  })

  it('switches the method body when another radio is picked', () => {
    renderPane(stateAt({}))

    fireEvent.click(radio(/Device code/))
    expect(screen.getByRole('button', { name: 'Get a device code' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Sign in with ChatGPT' })).toBeNull()

    fireEvent.click(radio(/Import JSON/))
    expect(screen.getByLabelText('Codex credentials')).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Import credentials' }) as HTMLButtonElement).disabled).toBe(true)

    fireEvent.click(radio(/Import from file/))
    expect(screen.getByRole('button', { name: 'Choose file…' })).toBeTruthy()
    expect(screen.queryByLabelText('Codex credentials')).toBeNull()
  })

  it('moves the radio selection with the keyboard and focuses the new card', () => {
    renderPane(stateAt({}))
    const group = screen.getByRole('radiogroup')

    fireEvent.keyDown(group, { key: 'ArrowDown' })
    expect(radio(/Device code/).getAttribute('aria-checked')).toBe('true')
    expect(document.activeElement?.textContent).toContain('Device code')

    fireEvent.keyDown(group, { key: 'ArrowUp' })
    expect(radio(/Official sign-in/).getAttribute('aria-checked')).toBe('true')
    expect(document.activeElement?.textContent).toContain('Official sign-in')

    // ArrowUp from the first entry wraps around to the last.
    fireEvent.keyDown(group, { key: 'ArrowUp' })
    expect(radio(/Import from file/).getAttribute('aria-checked')).toBe('true')
    expect(document.activeElement?.textContent).toContain('Import from file')

    fireEvent.keyDown(group, { key: 'Home' })
    expect(radio(/Official sign-in/).getAttribute('aria-checked')).toBe('true')
    expect(document.activeElement?.textContent).toContain('Official sign-in')

    fireEvent.keyDown(group, { key: 'End' })
    expect(radio(/Import from file/).getAttribute('aria-checked')).toBe('true')
    expect(document.activeElement?.textContent).toContain('Import from file')
  })

  it('shows the linked account on the manage card with email and plan', () => {
    renderPane(
      stateAt({ account: accountAt({ state: 'signed_in', email: 'dev@example.com', plan: 'Pro' }) }),
      true,
    )

    expect(screen.getByText('Signed in')).toBeTruthy()
    expect(screen.getByText('Email')).toBeTruthy()
    expect(screen.getByText('dev@example.com')).toBeTruthy()
    expect(screen.getByText('Plan')).toBeTruthy()
    expect(screen.getByText('Pro')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'View in list' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Disconnect' })).toBeTruthy()
    // A linked, signed-in account is not a sign-in affordance: no method picker.
    expect(screen.queryByRole('radiogroup')).toBeNull()
    expect(screen.queryByText('Signed out')).toBeNull()
  })

  it('keeps offering sign-in when the linked session fell out', () => {
    renderPane(
      stateAt({ account: accountAt({ state: 'reauth_needed', email: 'dev@example.com' }) }),
      true,
    )

    expect(screen.getByText('Sign-in needed')).toBeTruthy()
    expect(screen.getByText('The session expired. Sign in again to keep the provider working.')).toBeTruthy()
    expect(screen.getByText('dev@example.com')).toBeTruthy()
    expect(screen.getByRole('radiogroup')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Sign in with ChatGPT' })).toBeTruthy()
  })

  it('narrates the browser hand-off while the start call is in flight', () => {
    renderPane(stateAt({ loginPhase: 'connecting', activeMethod: 'browser' }))

    const opening = screen.getByRole('button', { name: 'Opening browser…' }) as HTMLButtonElement
    expect(opening.disabled).toBe(true)
    expect(screen.queryByRole('status')).toBeNull()
    // A live flow pins the picker: no radios to switch mid-flight.
    expect(screen.queryByRole('radiogroup')).toBeNull()
  })

  it('waits for the browser approval and can re-open the sign-in page', () => {
    renderPane(stateAt({
      loginPhase: 'waiting',
      activeMethod: 'browser',
      authorizeUrl: 'https://auth.openai.com/oauth/authorize',
    }))

    expect(screen.getByRole('status').textContent).toContain('Waiting for sign-in…')
    expect(screen.getByText('Approve the sign-in in the browser window that opened, then return here.')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Open the sign-in page again' }))
    expect(mocks.reopenAuthorizeUrl).toHaveBeenCalledTimes(1)
  })

  it('stays quiet about re-opening when no authorize URL is held', () => {
    renderPane(stateAt({ loginPhase: 'waiting', activeMethod: 'browser', authorizeUrl: '' }))

    expect(screen.getByRole('status').textContent).toContain('Waiting for sign-in…')
    expect(screen.queryByRole('button', { name: 'Open the sign-in page again' })).toBeNull()
  })

  it('starts the device flow from the device body', () => {
    renderPane(stateAt({}))

    fireEvent.click(radio(/Device code/))
    fireEvent.click(screen.getByRole('button', { name: 'Get a device code' }))
    expect(mocks.startDeviceLogin).toHaveBeenCalledTimes(1)
    expect(mocks.startLogin).not.toHaveBeenCalled()
  })

  it('narrates the device code request while it is in flight', () => {
    renderPane(stateAt({ loginPhase: 'connecting', activeMethod: 'device' }))

    const requesting = screen.getByRole('button', { name: 'Requesting a device code…' }) as HTMLButtonElement
    expect(requesting.disabled).toBe(true)
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('shows the device code with a copy button and an explicit verification link', async () => {
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
    })
    renderPane(stateAt({
      loginPhase: 'waiting',
      activeMethod: 'device',
      deviceUserCode: 'WLXB-DQK2',
      deviceVerificationUrl: 'https://auth.openai.com/codex/device',
    }))

    expect(screen.getByRole('status').textContent).toContain('Waiting for approval…')
    expect(screen.getByText('WLXB-DQK2')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Copy code' }))
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith('WLXB-DQK2')
    // The confirmation label lands once the clipboard promise settles.
    expect(await screen.findByRole('button', { name: 'Copied' })).toBeTruthy()

    // The verification page opens on an explicit click only.
    fireEvent.click(screen.getByRole('button', { name: 'Open the verification page' }))
    expect(mocks.openVerificationUrl).toHaveBeenCalledTimes(1)
  })

  it('keeps waiting for the device approval even when the code is not held', () => {
    renderPane(stateAt({ loginPhase: 'waiting', activeMethod: 'device' }))

    expect(screen.getByRole('status').textContent).toContain('Waiting for approval…')
    expect(screen.queryByRole('button', { name: 'Copy code' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Open the verification page' })).toBeNull()
  })

  it('imports pasted credentials only once there is text to import', () => {
    renderPane(stateAt({}))

    fireEvent.click(radio(/Import JSON/))
    const paste = screen.getByLabelText('Codex credentials')
    const importButton = screen.getByRole('button', { name: 'Import credentials' }) as HTMLButtonElement
    expect(importButton.disabled).toBe(true)

    fireEvent.change(paste, { target: { value: '{"access_token":"tok"}' } })
    expect(importButton.disabled).toBe(false)

    fireEvent.click(importButton)
    expect(mocks.importFromJson).toHaveBeenCalledWith('{"access_token":"tok"}')
  })

  it('narrates an import while it is in flight, for both import methods', () => {
    renderPane(stateAt({ loginPhase: 'connecting', activeMethod: 'importJson' }))

    const importing = screen.getByRole('button', { name: 'Importing credentials…' }) as HTMLButtonElement
    expect(importing.disabled).toBe(true)
    cleanup()

    renderPane(stateAt({ loginPhase: 'connecting', activeMethod: 'importFile' }))
    expect((screen.getByRole('button', { name: 'Importing credentials…' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('imports the picked native files', async () => {
    mocks.pickAuthFiles.mockResolvedValue(['C:\\Users\\dev\\.codex\\auth.json'])
    renderPane(stateAt({}))

    fireEvent.click(radio(/Import from file/))
    fireEvent.click(screen.getByRole('button', { name: 'Choose file…' }))

    await waitFor(() => expect(mocks.importFromFiles).toHaveBeenCalledWith(['C:\\Users\\dev\\.codex\\auth.json']))
    expect(mocks.importFromJson).not.toHaveBeenCalled()
  })

  it('does nothing when the native picker is dismissed', async () => {
    mocks.pickAuthFiles.mockResolvedValue([])
    renderPane(stateAt({}))

    fireEvent.click(radio(/Import from file/))
    fireEvent.click(screen.getByRole('button', { name: 'Choose file…' }))
    await waitFor(() => expect(mocks.pickAuthFiles).toHaveBeenCalledTimes(1))
    await Promise.resolve()

    expect(mocks.importFromFiles).not.toHaveBeenCalled()
    expect(mocks.importFromJson).not.toHaveBeenCalled()
  })

  it('falls back to a plain file input outside the desktop shell', async () => {
    mocks.pickAuthFiles.mockResolvedValue(null)
    renderPane(stateAt({}))

    fireEvent.click(radio(/Import from file/))
    fireEvent.click(screen.getByRole('button', { name: 'Choose file…' }))
    await waitFor(() => expect(mocks.pickAuthFiles).toHaveBeenCalledTimes(1))

    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    expect(input).toBeTruthy()
    const first = new File(['{"a":1}'], 'auth.json', { type: 'application/json' })
    Object.defineProperty(first, 'text', { value: () => Promise.resolve('{"a":1}') })
    fireEvent.change(input, { target: { files: [first] } })
    await waitFor(() => expect(mocks.importFromJson).toHaveBeenCalledWith('{"a":1}'))

    // The input is cleared after each pick, so the same file can be chosen again.
    const second = new File(['{"a":2}'], 'auth.json', { type: 'application/json' })
    Object.defineProperty(second, 'text', { value: () => Promise.resolve('{"a":2}') })
    fireEvent.change(input, { target: { files: [second] } })
    await waitFor(() => expect(mocks.importFromJson).toHaveBeenCalledWith('{"a":2}'))
  })

  it('reports a file that could not be read', async () => {
    mocks.pickAuthFiles.mockResolvedValue(null)
    renderPane(stateAt({}))

    fireEvent.click(radio(/Import from file/))
    fireEvent.click(screen.getByRole('button', { name: 'Choose file…' }))

    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    const broken = new File(['{}'], 'auth.json', { type: 'application/json' })
    Object.defineProperty(broken, 'text', { value: () => Promise.reject(new Error('unreadable')) })
    fireEvent.change(input, { target: { files: [broken] } })

    await waitFor(() => expect(screen.getByText('The file could not be read.')).toBeTruthy())
    expect(mocks.importFromJson).not.toHaveBeenCalled()
  })

  it('narrates the account exchange as a status line, not a dead end, whatever the method', () => {
    renderPane(stateAt({ loginPhase: 'exchanging', activeMethod: 'importFile' }))

    expect(screen.getByRole('status').textContent).toContain('Finishing sign-in…')
    expect(screen.getByText('The account is being linked and the provider is being created.')).toBeTruthy()
  })

  it('reports the signed-in account and says the provider was added after a fresh sign-in', () => {
    renderPane(
      stateAt({
        loginPhase: 'success',
        activeMethod: 'browser',
        account: accountAt({ state: 'signed_in', email: 'dev@example.com', plan: 'Pro' }),
      }),
      false,
    )

    expect(successLine('dev@example.com')).toBeTruthy()
    expect(screen.getByText('Pro')).toBeTruthy()
    expect(screen.getByText('The Codex provider was added to the list and is enabled.')).toBeTruthy()
  })

  it('says where an imported sign-in came from', () => {
    renderPane(
      stateAt({
        loginPhase: 'success',
        activeMethod: 'importFile',
        account: accountAt({ state: 'signed_in', email: 'dev@example.com', plan: 'Plus' }),
        importedFrom: 'auth.json',
      }),
      false,
    )

    expect(successLine('dev@example.com')).toBeTruthy()
    expect(screen.getByText('Imported from auth.json.')).toBeTruthy()
    expect(screen.getByText('The Codex provider was added to the list and is enabled.')).toBeTruthy()
  })

  it('keeps the manage card after success when the provider was already linked', () => {
    renderPane(
      stateAt({
        loginPhase: 'success',
        activeMethod: 'browser',
        account: accountAt({ state: 'signed_in', email: 'dev@example.com' }),
      }),
      true,
    )

    expect(successLine('dev@example.com')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'View in list' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Disconnect' })).toBeTruthy()
    // The row already existed; "was added" would be a lie here.
    expect(screen.queryByText('The Codex provider was added to the list and is enabled.')).toBeNull()
  })

  it('explains a failed start, keeps the failed method selected and offers to try again', () => {
    renderPane(stateAt({
      loginPhase: 'error',
      loginError: 'The control plane did not answer in time. Check that the relay is running, then try again.',
      activeMethod: 'browser',
    }))

    expect(screen.getByRole('alert').textContent).toContain('The control plane did not answer in time')
    // The failure came from the browser flow, so that method stays picked.
    expect(radio(/Official sign-in/).getAttribute('aria-checked')).toBe('true')
    fireEvent.click(screen.getByRole('button', { name: 'Sign in with ChatGPT' }))
    expect(mocks.startLogin).toHaveBeenCalledTimes(1)
  })

  it('retires a shown failure when the user switches to another method', () => {
    const view = renderPane(stateAt({ loginPhase: 'error', loginError: 'Bad tokens.', activeMethod: 'device' }))

    // The failure came from the device flow; the alert sits above the picker.
    expect(screen.getByRole('alert').textContent).toContain('Bad tokens.')
    expect(radio(/Device code/).getAttribute('aria-checked')).toBe('true')

    fireEvent.click(radio(/Import JSON/))
    expect(mocks.acknowledgeOutcome).toHaveBeenCalledTimes(1)

    // Once the outcome is acknowledged the pane-local pick takes over again.
    mocks.state = stateAt({})
    view.rerender(<CodexConnectPane providerExists={false} onSelectCodexRow={vi.fn()} onDisconnect={vi.fn()} />)
    expect(radio(/Import JSON/).getAttribute('aria-checked')).toBe('true')
    expect(screen.getByLabelText('Codex credentials')).toBeTruthy()
  })

  it('delegates viewing the row and disconnecting to the page', () => {
    const onSelectCodexRow = vi.fn()
    const onDisconnect = vi.fn()
    mocks.state = stateAt({ account: accountAt({ state: 'signed_in', email: 'dev@example.com' }) })
    render(
      <CodexConnectPane providerExists onDisconnect={onDisconnect} onSelectCodexRow={onSelectCodexRow} />,
    )

    fireEvent.click(screen.getByRole('button', { name: 'View in list' }))
    expect(onSelectCodexRow).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByRole('button', { name: 'Disconnect' }))
    expect(onDisconnect).toHaveBeenCalledTimes(1)
  })

  it('keeps the connect card a fresh login started from, even after the row appears mid-wait', () => {
    // A login started with no provider must not suddenly grow a manage card
    // just because the backend created the row mid-wait.
    mocks.state = stateAt({ loginPhase: 'waiting', activeMethod: 'browser' })
    const view = render(
      <CodexConnectPane providerExists={false} onSelectCodexRow={vi.fn()} onDisconnect={vi.fn()} />,
    )
    view.rerender(<CodexConnectPane providerExists onSelectCodexRow={vi.fn()} onDisconnect={vi.fn()} />)

    expect(screen.getByRole('status').textContent).toContain('Waiting for sign-in…')
    expect(screen.queryByRole('button', { name: 'View in list' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Disconnect' })).toBeNull()
  })

  it('keeps the manage card for a still-linked account even if the provider row disappears', () => {
    mocks.state = stateAt({
      loginPhase: 'waiting',
      activeMethod: 'browser',
      account: accountAt({ state: 'signed_in', email: 'dev@example.com' }),
    })
    const view = render(
      <CodexConnectPane providerExists onDisconnect={vi.fn()} onSelectCodexRow={vi.fn()} />,
    )
    view.rerender(<CodexConnectPane providerExists={false} onDisconnect={vi.fn()} onSelectCodexRow={vi.fn()} />)

    expect(screen.getByRole('button', { name: 'View in list' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Disconnect' })).toBeTruthy()
    expect(screen.getByText('dev@example.com')).toBeTruthy()
  })
})
