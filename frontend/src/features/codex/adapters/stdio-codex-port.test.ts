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

  it('reads the account rows the backend reports with their aggregate state', async () => {
    const session = sessionWith({
      'codex.status': {
        state: 'signed_in',
        accounts: [
          { state: 'signed_in', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex' },
          { state: 'reauth_needed', email: 'sam@example.com', plan: 'Plus', accountId: 'acct-2', providerId: 'codex' },
        ],
      },
    })
    const port = new StdioCodexPort(session)

    await expect(port.status()).resolves.toEqual({
      state: 'signed_in',
      accounts: [
        { state: 'signed_in', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex' },
        { state: 'reauth_needed', email: 'sam@example.com', plan: 'Plus', accountId: 'acct-2', providerId: 'codex' },
      ],
    })
    expect(vi.mocked(session.call)).toHaveBeenLastCalledWith('codex.status', undefined, undefined)
  })

  it('fails safe when the status payload is malformed: rows survive, guesses do not', async () => {
    // The aggregate state is a function of the rows, so a broken state field
    // is recomputed rather than wiping real accounts away.
    const unknownState = new StdioCodexPort(sessionWith({
      'codex.status': {
        state: 'linked',
        accounts: [{ state: 'signed_in', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex' }],
      },
    }))
    await expect(unknownState.status()).resolves.toEqual({
      state: 'signed_in',
      accounts: [{ state: 'signed_in', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex' }],
    })

    // Accounts that are not a list cannot be coerced into rows: nothing
    // carries over from a payload the contract did not describe.
    const notAnArray = new StdioCodexPort(sessionWith({ 'codex.status': { state: 'signed_in', accounts: 'nope' } }))
    await expect(notAnArray.status()).resolves.toEqual({ state: 'signed_out', accounts: [] })

    // Per-account fields coerce like they always did: the row survives and
    // the broken fields empty out; an unknown account state reads signed-out.
    const malformed = new StdioCodexPort(sessionWith({
      'codex.status': {
        state: 'signed_in',
        accounts: [{ state: 'linked', email: 42, plan: null, accountId: ['a'], providerId: {} }],
      },
    }))
    await expect(malformed.status()).resolves.toEqual({
      state: 'signed_in',
      accounts: [{ state: 'signed_out', email: '', plan: '', accountId: '', providerId: '' }],
    })

    // An entry that is not an object is not an account: dropped, not coerced
    // into a phantom row.
    const strayEntry = new StdioCodexPort(sessionWith({
      'codex.status': {
        state: 'signed_in',
        accounts: [
          'nope',
          { state: 'signed_in', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex' },
        ],
      },
    }))
    await expect(strayEntry.status()).resolves.toEqual({
      state: 'signed_in',
      accounts: [{ state: 'signed_in', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex' }],
    })
  })

  it('probes one account by its id and reads the report with both windows; a refusal is a result field', async () => {
    const session = sessionWith({
      'codex.quota': {
        accountId: 'acct-1',
        quota: {
          fetchedAt: 1_789_000_000, planType: 'Pro',
          primary: { present: true, remainingPercent: 78.4, windowMinutes: 300, resetAt: 1_789_003_120 },
          secondary: { present: true, remainingPercent: 41, windowMinutes: 10_080, resetAt: 1_789_172_800 },
        },
      },
    })
    const port = new StdioCodexPort(session)

    await expect(port.quota('acct-1')).resolves.toEqual({
      accountId: 'acct-1',
      quota: {
        fetchedAt: 1_789_000_000, planType: 'Pro',
        primary: { present: true, remainingPercent: 78, windowMinutes: 300, resetAt: 1_789_003_120 },
        secondary: { present: true, remainingPercent: 41, windowMinutes: 10_080, resetAt: 1_789_172_800 },
      },
      error: undefined,
    })
    // The answer carries no account identity of its own: the row it belongs
    // to is the one the caller asked about.
    expect(vi.mocked(session.call)).toHaveBeenLastCalledWith('codex.quota', { accountId: 'acct-1' }, undefined)
  })

  it('reads a quota refusal as the asked account with an error and no report', async () => {
    const port = new StdioCodexPort(sessionWith({
      'codex.quota': { accountId: 'acct-1', error: 'codex is not signed in' },
    }))
    await expect(port.quota('acct-1')).resolves.toEqual({
      accountId: 'acct-1', quota: undefined, error: 'codex is not signed in',
    })
  })

  it('fails safe on malformed quota data: a report needs a fetchedAt, a window needs a reported value', async () => {
    const noReport = new StdioCodexPort(sessionWith({
      'codex.quota': { accountId: 'acct-1', quota: { planType: 'Pro' }, error: 410 },
    }))
    // A non-text error stays silent rather than guessing what it meant.
    await expect(noReport.quota('acct-1')).resolves.toMatchObject({ accountId: 'acct-1', quota: undefined, error: undefined })

    const clamped = new StdioCodexPort(sessionWith({
      'codex.quota': {
        accountId: 'acct-1',
        quota: {
          fetchedAt: 5,
          primary: { present: true, remainingPercent: 300.6, windowMinutes: 0.5, resetAt: -1 },
          secondary: { present: 'yes' },
        },
      },
    }))
    const result = await clamped.quota('acct-1')
    expect(result.quota).toEqual({
      fetchedAt: 5,
      primary: { present: true, remainingPercent: 100 },
      secondary: { present: false, remainingPercent: 100 },
    })
  })

  it('issues cancel and an all-accounts logout as their own commands', async () => {
    const session = sessionWith({ 'codex.login.cancel': {}, 'codex.logout': {} })
    const port = new StdioCodexPort(session)

    await port.loginCancel()
    await port.logout(null, false)
    expect(session.call).toHaveBeenNthCalledWith(1, 'codex.login.cancel', undefined, undefined)
    // No target account and no removal flag: the frame stays exactly as the
    // single-account backend already understood it.
    expect(session.call).toHaveBeenNthCalledWith(2, 'codex.logout', undefined, undefined)
  })

  it('targets a logout at one account or every account, and asks for removal only when told', async () => {
    const session = sessionWith({ 'codex.logout': {} })
    const port = new StdioCodexPort(session)

    await port.logout('acct-1', false)
    expect(session.call).toHaveBeenNthCalledWith(1, 'codex.logout', { accountId: 'acct-1' }, undefined)

    await port.logout('acct-1', true)
    expect(session.call).toHaveBeenNthCalledWith(2, 'codex.logout', { accountId: 'acct-1', remove: true }, undefined)

    await port.logout(null, false)
    expect(session.call).toHaveBeenNthCalledWith(3, 'codex.logout', undefined, undefined)

    await port.logout(null, true)
    expect(session.call).toHaveBeenNthCalledWith(4, 'codex.logout', { remove: true }, undefined)
  })

  it('reads the import outcome: the state, the accounts it produced, and the file it came from', async () => {
    const port = new StdioCodexPort(sessionWith({
      'codex.import.json': {
        state: 'signed_in',
        accounts: [{ state: 'signed_in', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex' }],
        importedFrom: 'auth.json',
      },
    }))
    await expect(port.importJson('{}')).resolves.toEqual({
      state: 'signed_in',
      accounts: [{ state: 'signed_in', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex' }],
      importedFrom: 'auth.json',
    })

    // A pasted payload has no source file to name.
    const files = new StdioCodexPort(sessionWith({
      'codex.import.files': {
        state: 'signed_in',
        accounts: [
          { state: 'signed_in', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex' },
          { state: 'signed_in', email: 'sam@example.com', plan: 'Plus', accountId: 'acct-2', providerId: 'codex' },
        ],
      },
    }))
    await expect(files.importFiles(['C:\\auth.json'])).resolves.toEqual({
      state: 'signed_in',
      accounts: [
        { state: 'signed_in', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex' },
        { state: 'signed_in', email: 'sam@example.com', plan: 'Plus', accountId: 'acct-2', providerId: 'codex' },
      ],
      importedFrom: undefined,
    })
  })

  it('fails an import safe: no accounts appear from a payload the contract did not describe', async () => {
    const port = new StdioCodexPort(sessionWith({ 'codex.import.json': { state: 'signed_in', accounts: 'nope' } }))
    await expect(port.importJson('{}')).resolves.toEqual({ state: 'signed_out', accounts: [], importedFrom: undefined })
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
