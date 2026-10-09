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

  it('reads the quota report with both windows, and a refusal arrives as a result field, not a rejection', async () => {
    const port = new StdioCodexPort(sessionWith({
      'codex.quota': {
        state: 'signed_in', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex',
        quota: {
          fetchedAt: 1_789_000_000, planType: 'Pro',
          primary: { present: true, remainingPercent: 78.4, windowMinutes: 300, resetAt: 1_789_003_120 },
          secondary: { present: true, remainingPercent: 41, windowMinutes: 10_080, resetAt: 1_789_172_800 },
        },
      },
    }))
    await expect(port.quota()).resolves.toEqual({
      state: 'signed_in', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex',
      quota: {
        fetchedAt: 1_789_000_000, planType: 'Pro',
        primary: { present: true, remainingPercent: 78, windowMinutes: 300, resetAt: 1_789_003_120 },
        secondary: { present: true, remainingPercent: 41, windowMinutes: 10_080, resetAt: 1_789_172_800 },
      },
      error: undefined,
    })

    const refused = new StdioCodexPort(sessionWith({
      'codex.quota': { state: 'signed_out', email: '', plan: '', accountId: '', providerId: '', error: 'codex is not signed in' },
    }))
    await expect(refused.quota()).resolves.toEqual({
      state: 'signed_out', email: '', plan: '', accountId: '', providerId: '',
      quota: undefined, error: 'codex is not signed in',
    })
  })

  it('fails safe on malformed quota data: a report needs a fetchedAt, a window needs a reported value', async () => {
    const noReport = new StdioCodexPort(sessionWith({
      'codex.quota': { state: 'signed_in', quota: { planType: 'Pro' }, error: 410 },
    }))
    // A non-text error stays silent rather than guessing what it meant.
    await expect(noReport.quota()).resolves.toMatchObject({ quota: undefined, error: undefined })

    const clamped = new StdioCodexPort(sessionWith({
      'codex.quota': {
        state: 'signed_in',
        quota: {
          fetchedAt: 5,
          primary: { present: true, remainingPercent: 300.6, windowMinutes: 0.5, resetAt: -1 },
          secondary: { present: 'yes' },
        },
      },
    }))
    const result = await clamped.quota()
    expect(result.quota).toEqual({
      fetchedAt: 5,
      primary: { present: true, remainingPercent: 100 },
      secondary: { present: false, remainingPercent: 100 },
    })
  })

  it('issues cancel and logout as their own commands', async () => {
    const session = sessionWith({ 'codex.login.cancel': {}, 'codex.logout': {} })
    const port = new StdioCodexPort(session)

    await port.loginCancel()
    await port.logout(false)
    expect(session.call).toHaveBeenNthCalledWith(1, 'codex.login.cancel', undefined, undefined)
    expect(session.call).toHaveBeenNthCalledWith(2, 'codex.logout', undefined, undefined)
  })

  it('asks the logout to remove the provider only when told to', async () => {
    const session = sessionWith({ 'codex.logout': {} })
    const port = new StdioCodexPort(session)

    // A disconnect sends the empty payload the backend already knows; the
    // delete is the one optional field codex.logout accepts, so the frame
    // grows exactly that flag and nothing else.
    await port.logout(false)
    expect(session.call).toHaveBeenNthCalledWith(1, 'codex.logout', undefined, undefined)

    await port.logout(true)
    expect(session.call).toHaveBeenNthCalledWith(2, 'codex.logout', { remove: true }, undefined)
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
