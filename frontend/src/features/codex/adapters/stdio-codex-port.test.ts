import { describe, expect, it, vi } from 'vitest'
import { StdioCodexPort } from './stdio-codex-port'
import { ControlPlaneError } from '../../../shared/contracts/protocol'
import type { ControlPlaneSession } from '../../../platform/stdio/session'

// The session mock answers per command so each test pins the command the
// adapter actually issues, not just the value it hands back.
function sessionWith(answers: Record<string, unknown>): ControlPlaneSession {
  return {
    start: vi.fn(async () => undefined),
    call: vi.fn(async (command: string) => {
      if (!(command in answers)) throw new Error(`unexpected command ${command}`)
      return answers[command]
    }) as ControlPlaneSession['call'],
    subscribe: vi.fn(() => () => undefined),
    stop: vi.fn(async () => undefined),
  }
}

describe('StdioCodexPort', () => {
  it('returns the authorize URL the backend issued, or an empty one when absent', async () => {
    const port = new StdioCodexPort(sessionWith({ 'codex.login.start': { authorizeUrl: 'https://auth.openai.com/authorize' } }))
    await expect(port.loginStart()).resolves.toEqual({ authorizeUrl: 'https://auth.openai.com/authorize' })

    const bare = new StdioCodexPort(sessionWith({ 'codex.login.start': {} }))
    await expect(bare.loginStart()).resolves.toEqual({ authorizeUrl: '' })
  })

  it('reads the login phase and surfaces only a string error', async () => {
    const port = new StdioCodexPort(sessionWith({
      'codex.login.status': { phase: 'waiting', error: 'provider unavailable' },
    }))
    await expect(port.loginStatus()).resolves.toEqual({ phase: 'waiting', error: 'provider unavailable' })
  })

  it('fails safe to idle on an unknown phase and drops a non-string error', async () => {
    const port = new StdioCodexPort(sessionWith({
      'codex.login.status': { phase: 'handshaking', error: { code: 12 } },
    }))
    await expect(port.loginStatus()).resolves.toEqual({ phase: 'idle', error: undefined })
  })

  it('reads the flat account payload the backend reports', async () => {
    const port = new StdioCodexPort(sessionWith({
      'codex.status': { state: 'signed_in', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex' },
    }))
    await expect(port.status()).resolves.toEqual({
      state: 'signed_in', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex',
    })
  })

  it('fails safe to a fully signed-out account when the state is unknown or fields are malformed', async () => {
    const unknownState = new StdioCodexPort(sessionWith({ 'codex.status': { state: 'linked' } }))
    await expect(unknownState.status()).resolves.toEqual({
      state: 'signed_out', email: '', plan: '', accountId: '', providerId: '',
    })

    const malformed = new StdioCodexPort(sessionWith({
      'codex.status': { state: 'signed_in', email: 42, plan: null, accountId: ['a'], providerId: {} },
    }))
    await expect(malformed.status()).resolves.toEqual({
      state: 'signed_in', email: '', plan: '', accountId: '', providerId: '',
    })
  })

  it('issues cancel and logout as their own commands', async () => {
    const session = sessionWith({ 'codex.login.cancel': {}, 'codex.logout': {} })
    const port = new StdioCodexPort(session)

    await port.loginCancel()
    await port.logout()
    expect(session.call).toHaveBeenNthCalledWith(1, 'codex.login.cancel', undefined, undefined)
    expect(session.call).toHaveBeenNthCalledWith(2, 'codex.logout', undefined, undefined)
  })

  it('opens the authorize URL through the injected opener only when it is HTTPS', async () => {
    const openUrl = vi.fn(async () => undefined)
    const port = new StdioCodexPort(sessionWith({}), openUrl)

    await port.openAuthorizeUrl('https://auth.openai.com/reauthorize')
    expect(openUrl).toHaveBeenCalledWith('https://auth.openai.com/reauthorize')

    await expect(port.openAuthorizeUrl('http://auth.openai.com/reauthorize')).rejects.toBeInstanceOf(ControlPlaneError)
    await expect(port.openAuthorizeUrl('not a url')).rejects.toMatchObject({ code: 'insecure_url' })
    expect(openUrl).toHaveBeenCalledTimes(1)
  })

  it('bridges codex.changed pushes to the port subscription', () => {
    const session = sessionWith({})
    const port = new StdioCodexPort(session)
    const listener = () => undefined
    const unsubscribe = port.subscribe(listener)

    expect(session.subscribe).toHaveBeenCalledWith('codex.changed', listener)
    expect(unsubscribe).toBeInstanceOf(Function)
  })
})
