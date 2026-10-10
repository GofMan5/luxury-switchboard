import { describe, expect, it } from 'vitest'
import { ControlPlaneError } from '../../../shared/contracts/protocol'
import type { CodexAccount, CodexLoginStatus, CodexQuotaReport, CodexQuotaResult, CodexStatus } from '../domain/codex'
import type { CodexDeviceLoginStart, CodexImportResult, CodexPort } from './codex-port'
import { CodexModel } from './codex-model'

const signedIn: CodexAccount = { state: 'signed_in', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex-1' }
const secondSignedIn: CodexAccount = { state: 'signed_in', email: 'sam@example.com', plan: 'Plus', accountId: 'acct-2', providerId: 'codex-1' }
const needsReauth: CodexAccount = { state: 'reauth_needed', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex-1' }

const usage: CodexQuotaReport = {
  fetchedAt: 1_789_000_000,
  planType: 'Pro',
  primary: { present: true, remainingPercent: 78, windowMinutes: 300, resetAt: 1_789_003_120 },
  secondary: { present: true, remainingPercent: 41, windowMinutes: 10_080, resetAt: 1_789_172_800 },
}

class FakeCodexPort implements CodexPort {
  statusResult: CodexStatus = { state: 'signed_out', accounts: [] }
  login: CodexLoginStatus = { phase: 'idle' }
  authorizeUrl = 'https://auth.openai.com/authorize'
  loginStartCalls = 0
  loginCancelCalls = 0
  logoutCalls = 0
  logoutIDs: (string | null)[] = []
  logoutRemoveFlags: boolean[] = []
  openUrls: readonly string[] = []
  openAuthorizeUrlError: Error | null = null
  loginStartError: Error | null = null
  logoutError: Error | null = null
  abortSignal: AbortSignal | null = null
  listener: (() => void) | null = null
  /** When set, loginStart stays pending until the test resolves it. */
  loginStartDelay: Promise<void> | null = null

  // Device flow: what codex.login.device.start hands back.
  deviceLoginStartCalls = 0
  deviceLoginStartError: Error | null = null
  deviceLoginStartDelay: Promise<void> | null = null
  deviceStart: CodexDeviceLoginStart = { userCode: 'WDJB-MJCD', verificationUrl: 'https://auth.openai.com/codex/device', pollIntervalSeconds: 5 }

  // Imports: the text/paths seen by the port, and what they resolve to.
  importJsonCalls: readonly string[] = []
  importFilesCalls: readonly (readonly string[])[] = []
  importJsonResult: CodexImportResult | null = null
  importFilesResult: CodexImportResult | null = null
  importJsonError: Error | null = null
  importFilesError: Error | null = null
  importDelay: Promise<void> | null = null

  // Usage probe: what codex.quota answers per account, and how it can fail.
  quotaResults: Record<string, CodexQuotaResult> = {}
  quotaError: Error | null = null
  quotaCalls: string[] = []
  /** When set, quota stays pending until the test resolves it. */
  quotaDelay: Promise<void> | null = null

  async loginStart(signal?: AbortSignal): Promise<{ authorizeUrl: string }> {
    this.loginStartCalls += 1
    this.abortSignal = signal ?? null
    await this.loginStartDelay
    if (this.loginStartError) throw this.loginStartError
    return { authorizeUrl: this.authorizeUrl }
  }

  async loginStatus(): Promise<CodexLoginStatus> { return this.login }
  async loginCancel(): Promise<void> { this.loginCancelCalls += 1 }
  async status(): Promise<CodexStatus> { return this.statusResult }

  async deviceLoginStart(signal?: AbortSignal): Promise<CodexDeviceLoginStart> {
    this.deviceLoginStartCalls += 1
    this.abortSignal = signal ?? null
    await this.deviceLoginStartDelay
    if (this.deviceLoginStartError) throw this.deviceLoginStartError
    return this.deviceStart
  }

  async importJson(text: string, signal?: AbortSignal): Promise<CodexImportResult> {
    this.importJsonCalls = [...this.importJsonCalls, text]
    this.abortSignal = signal ?? null
    await this.importDelay
    if (this.importJsonError) throw this.importJsonError
    return this.importJsonResult ?? { state: 'signed_in', accounts: [signedIn] }
  }

  async importFiles(paths: readonly string[], signal?: AbortSignal): Promise<CodexImportResult> {
    this.importFilesCalls = [...this.importFilesCalls, [...paths]]
    this.abortSignal = signal ?? null
    await this.importDelay
    if (this.importFilesError) throw this.importFilesError
    return this.importFilesResult ?? { state: 'signed_in', accounts: [signedIn], importedFrom: 'auth.json' }
  }
  async logout(accountId: string | null, remove: boolean): Promise<void> {
    this.logoutCalls += 1
    this.logoutIDs.push(accountId)
    this.logoutRemoveFlags.push(remove)
    if (this.logoutError) throw this.logoutError
  }

  async quota(accountId: string): Promise<CodexQuotaResult> {
    this.quotaCalls.push(accountId)
    await this.quotaDelay
    if (this.quotaError) throw this.quotaError
    return this.quotaResults[accountId] ?? { accountId, error: 'codex is not signed in' }
  }

  async openAuthorizeUrl(url: string): Promise<void> {
    this.openUrls = [...this.openUrls, url]
    if (this.openAuthorizeUrlError) throw this.openAuthorizeUrlError
  }

  subscribe(listener: () => void) {
    this.listener = listener
    return () => { this.listener = null }
  }
}

describe('CodexModel', () => {
  it('connects once and loads the accounts and login phase', async () => {
    const port = new FakeCodexPort()
    port.statusResult = { state: 'signed_in', accounts: [signedIn] }
    const model = new CodexModel(port)
    await model.connect()
    await model.connect()
    expect(port.listener).not.toBeNull()
    expect(model.snapshot()).toMatchObject({
      loginPhase: 'idle',
      state: 'signed_in',
      freshAccount: null,
      loginError: '',
      authorizeUrl: '',
    })
    expect(model.snapshot().accounts).toEqual([signedIn])
    model.dispose()
  })

  it('refetches when codex.changed nudges', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    expect(model.snapshot().state).toBe('signed_out')
    expect(model.snapshot().accounts).toEqual([])
    port.statusResult = { state: 'signed_in', accounts: [signedIn] }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot().state).toBe('signed_in')
    expect(model.snapshot().accounts).toEqual([signedIn])
    model.dispose()
  })

  it('reports the aggregate state the backend computed, and the rows that justify it', async () => {
    const port = new FakeCodexPort()
    port.statusResult = { state: 'signed_in', accounts: [signedIn, { ...secondSignedIn, state: 'reauth_needed' }] }
    const model = new CodexModel(port)
    await model.connect()
    // One healthy row keeps the aggregate signed in; the broken row stays visible on its own.
    expect(model.snapshot().state).toBe('signed_in')
    expect(model.snapshot().accounts).toHaveLength(2)
    expect(model.snapshot().accounts[1]).toMatchObject({ state: 'reauth_needed', accountId: 'acct-2' })
    model.dispose()

    const allBroken = new FakeCodexPort()
    allBroken.statusResult = { state: 'reauth_needed', accounts: [needsReauth] }
    const degraded = new CodexModel(allBroken)
    await degraded.connect()
    expect(degraded.snapshot().state).toBe('reauth_needed')
    degraded.dispose()
  })

  it('starts a login: connecting, then waiting, and opens the authorize URL', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    void model.startLogin()
    expect(model.snapshot()).toMatchObject({ loginPhase: 'connecting', activeMethod: 'browser' })
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot()).toMatchObject({ loginPhase: 'waiting', authorizeUrl: 'https://auth.openai.com/authorize' })
    expect(port.openUrls).toEqual([port.authorizeUrl])
    model.dispose()
  })

  it('ignores a second start while one is already waiting', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    await model.startLogin()
    expect(model.snapshot().loginPhase).toBe('waiting')
    await model.startLogin()
    expect(port.loginStartCalls).toBe(1)
    model.dispose()
  })

  it('maps a sidecar timeout to readable copy', async () => {
    const port = new FakeCodexPort()
    port.loginStartError = new ControlPlaneError('timeout', 'Sidecar command timed out')
    const model = new CodexModel(port)
    await model.connect()
    await model.startLogin()
    expect(model.snapshot()).toMatchObject({
      loginPhase: 'error',
      loginError: 'The control plane did not answer in time. Check that the relay is running, then try again.',
    })
    model.dispose()
  })

  it('surfaces the refused authorize URL as an error instead of opening it', async () => {
    const port = new FakeCodexPort()
    port.loginStartError = new ControlPlaneError('insecure_url', 'The sign-in address was not HTTPS.')
    const model = new CodexModel(port)
    await model.connect()
    await model.startLogin()
    expect(model.snapshot()).toMatchObject({ loginPhase: 'error', loginError: 'The sign-in address was not HTTPS.' })
    expect(port.openUrls).toEqual([])
    model.dispose()
  })

  it('maps a busy local sign-in port to readable copy', async () => {
    const port = new FakeCodexPort()
    port.loginStartError = new ControlPlaneError('command_failed', 'codex login listener: listen tcp 127.0.0.1:1455: bind: address already in use')
    const model = new CodexModel(port)
    await model.connect()
    await model.startLogin()
    expect(model.snapshot()).toMatchObject({
      loginPhase: 'error',
      loginError: 'The local sign-in port is already in use. Close the other sign-in attempt, then try again.',
    })
    model.dispose()
  })

  it('maps an unknown codex_login_failed message to the generic sign-in copy', async () => {
    const port = new FakeCodexPort()
    port.loginStartError = new ControlPlaneError('codex_login_failed', 'the OAuth state could not be generated')
    const model = new CodexModel(port)
    await model.connect()
    await model.startLogin()
    expect(model.snapshot()).toMatchObject({ loginPhase: 'error', loginError: 'Codex sign-in could not be started.' })
    model.dispose()
  })

  it('does not treat a near-miss code as a codex login failure', async () => {
    const port = new FakeCodexPort()
    port.loginStartError = new ControlPlaneError('command_failed', 'codex login listener: listen tcp 127.0.0.1:1455: bind: address already in use')
    const model = new CodexModel(port)
    await model.connect()
    await model.startLogin()
    expect(model.snapshot().loginError).toBe('The local sign-in port is already in use. Close the other sign-in attempt, then try again.')
    model.dispose()
  })

  it('maps a login timeout pushed as the backend phase error', async () => {
    const port = new FakeCodexPort()
    port.login = { phase: 'waiting' }
    const model = new CodexModel(port)
    await model.connect()
    port.login = { phase: 'error', error: 'codex login timed out' }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot()).toMatchObject({
      loginPhase: 'error',
      loginError: 'The sign-in timed out before the browser answered. Try again.',
    })
    model.dispose()
  })

  it('maps a login cancellation pushed as the backend phase error', async () => {
    const port = new FakeCodexPort()
    port.login = { phase: 'waiting' }
    const model = new CodexModel(port)
    await model.connect()
    port.login = { phase: 'error', error: 'codex login cancelled' }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot()).toMatchObject({
      loginPhase: 'error',
      loginError: 'The sign-in was cancelled or denied before it completed.',
    })
    model.dispose()
  })

  it('keeps an unknown phase error generic', async () => {
    const port = new FakeCodexPort()
    port.login = { phase: 'waiting' }
    const model = new CodexModel(port)
    await model.connect()
    port.login = { phase: 'error', error: 'codex login timed out badly' }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot()).toMatchObject({ loginPhase: 'error', loginError: 'Codex sign-in could not be completed.' })
    model.dispose()
  })

  it('maps a storage failure pushed as the backend phase error', async () => {
    const port = new FakeCodexPort()
    port.login = { phase: 'waiting' }
    const model = new CodexModel(port)
    await model.connect()
    port.login = { phase: 'error', error: 'codex session could not be stored' }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot()).toMatchObject({
      loginPhase: 'error',
      loginError: 'Codex signed in, but the session could not be saved. Try again.',
    })
    model.dispose()
  })

  it('maps a provisioning failure pushed as the backend phase error', async () => {
    const port = new FakeCodexPort()
    port.login = { phase: 'waiting' }
    const model = new CodexModel(port)
    await model.connect()
    port.login = { phase: 'error', error: 'codex provider could not be provisioned: database is locked' }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot()).toMatchObject({
      loginPhase: 'error',
      loginError: 'Codex signed in, but the provider could not be added to the list. Try again.',
    })
    model.dispose()
  })

  it('maps an exchange failure pushed as the backend phase error', async () => {
    const port = new FakeCodexPort()
    port.login = { phase: 'waiting' }
    const model = new CodexModel(port)
    await model.connect()
    port.login = { phase: 'error', error: 'codex oauth exchange failed: 400 invalid_grant' }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot()).toMatchObject({
      loginPhase: 'error',
      loginError: 'The sign-in could not be completed. Try again.',
    })
    model.dispose()
  })

  it('cancels a waiting login back to idle', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    await model.startLogin()
    expect(model.snapshot().loginPhase).toBe('waiting')
    await model.cancelLogin()
    expect(model.snapshot()).toMatchObject({ loginPhase: 'idle', activeMethod: null, authorizeUrl: '' })
    expect(port.loginCancelCalls).toBe(1)
    model.dispose()
  })

  it('aborts an in-flight start when cancelled', async () => {
    const port = new FakeCodexPort()
    let release: () => void = () => {}
    port.loginStartDelay = new Promise<void>((resolve) => { release = () => resolve() })
    const model = new CodexModel(port)
    await model.connect()
    const pending = model.startLogin()
    await model.cancelLogin()
    expect(port.abortSignal?.aborted).toBe(true)
    release()
    await pending
    expect(model.snapshot().loginPhase).toBe('idle')
    model.dispose()
  })

  it('keeps the authorize URL while the start is still pending', async () => {
    const port = new FakeCodexPort()
    let release: () => void = () => {}
    port.loginStartDelay = new Promise<void>((resolve) => { release = () => resolve() })
    const model = new CodexModel(port)
    await model.connect()
    const pending = model.startLogin()
    port.login = { phase: 'waiting' }
    port.statusResult = { state: 'signed_in', accounts: [signedIn] }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot()).toMatchObject({ loginPhase: 'waiting', authorizeUrl: '' })
    expect(port.openUrls).toEqual([])
    release()
    await pending
    expect(model.snapshot()).toMatchObject({ loginPhase: 'waiting', authorizeUrl: port.authorizeUrl })
    expect(port.openUrls).toEqual([port.authorizeUrl])
    model.dispose()
  })

  it('keeps a cancelled login idle when completion is pushed late', async () => {
    const port = new FakeCodexPort()
    port.login = { phase: 'waiting' }
    const model = new CodexModel(port)
    await model.connect()
    await model.cancelLogin()
    expect(model.snapshot().loginPhase).toBe('idle')
    port.login = { phase: 'exchanging' }
    port.statusResult = { state: 'signed_in', accounts: [signedIn] }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot()).toMatchObject({ loginPhase: 'idle', loginError: '', authorizeUrl: '' })
    expect(model.snapshot().state).toBe('signed_in')
    port.login = { phase: 'success' }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot().loginPhase).toBe('idle')
    model.dispose()
  })

  it('merges backend phases after a fresh startLogin', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    await model.startLogin()
    expect(model.snapshot().loginPhase).toBe('waiting')
    port.login = { phase: 'exchanging' }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot().loginPhase).toBe('exchanging')
    port.login = { phase: 'success' }
    port.statusResult = { state: 'signed_in', accounts: [signedIn] }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot().loginPhase).toBe('success')
    model.dispose()
  })

  it('treats the backend phase as truth after a restart', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    await model.startLogin()
    port.login = { phase: 'idle' }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot().loginPhase).toBe('idle')
    model.dispose()
  })

  it('settles back to idle after the user restarts and the backend says idle', async () => {
    const port = new FakeCodexPort()
    port.login = { phase: 'error', error: 'codex login timed out' }
    const model = new CodexModel(port)
    await model.connect()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot().loginPhase).toBe('error')
    port.login = { phase: 'idle' }
    await model.refresh()
    expect(model.snapshot().loginPhase).toBe('idle')
    model.dispose()
  })

  it('keeps an unacknowledged error when an unrelated push refetches', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    port.login = { phase: 'error', error: 'Port 1455 is busy' }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot().loginPhase).toBe('error')
    port.login = { phase: 'idle' }
    port.statusResult = { state: 'signed_in', accounts: [signedIn] }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot()).toMatchObject({ loginPhase: 'error', loginError: 'Codex sign-in could not be completed.' })
    expect(model.snapshot().state).toBe('signed_in')
    model.dispose()
  })

  it('narrows a sticky success phase to accounts that are still signed in', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    port.login = { phase: 'success' }
    port.statusResult = { state: 'signed_in', accounts: [signedIn] }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot().loginPhase).toBe('success')
    port.login = { phase: 'idle' }
    port.statusResult = { state: 'reauth_needed', accounts: [needsReauth] }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot().loginPhase).toBe('idle')
    model.dispose()
  })

  it('adds a second account without losing the first, and the new row is the fresh one', async () => {
    const port = new FakeCodexPort()
    port.statusResult = { state: 'signed_in', accounts: [signedIn] }
    const model = new CodexModel(port)
    await model.connect()
    port.login = { phase: 'success' }
    port.statusResult = { state: 'signed_in', accounts: [signedIn, secondSignedIn] }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot()).toMatchObject({ loginPhase: 'success', state: 'signed_in' })
    expect(model.snapshot().accounts).toEqual([signedIn, secondSignedIn])
    expect(model.snapshot().freshAccount).toEqual(secondSignedIn)
    model.dispose()
  })

  it('marks an account that recovered from reauth as fresh, whatever pushed it', async () => {
    const port = new FakeCodexPort()
    port.statusResult = { state: 'reauth_needed', accounts: [needsReauth] }
    const model = new CodexModel(port)
    await model.connect()
    expect(model.snapshot().freshAccount).toBeNull()
    port.statusResult = { state: 'signed_in', accounts: [signedIn] }
    await model.refresh()
    expect(model.snapshot().freshAccount).toEqual(signedIn)
    model.dispose()
  })

  it('acknowledgeOutcome resets the terminal phase and the fresh account, and is a no-op when idle', async () => {
    const port = new FakeCodexPort()
    port.login = { phase: 'success' }
    port.statusResult = { state: 'signed_in', accounts: [signedIn] }
    const model = new CodexModel(port)
    await model.connect()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot().loginPhase).toBe('success')
    model.acknowledgeOutcome()
    expect(model.snapshot()).toMatchObject({ loginPhase: 'idle', loginError: '', freshAccount: null })
    model.acknowledgeOutcome()
    expect(model.snapshot().loginPhase).toBe('idle')
    model.dispose()
  })

  it('acknowledgeOutcome ignores an in-flight login', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    void model.startLogin()
    model.acknowledgeOutcome()
    expect(model.snapshot().loginPhase).toBe('connecting')
    model.dispose()
  })

  it('logs out one account and refreshes the rows', async () => {
    const port = new FakeCodexPort()
    port.statusResult = { state: 'signed_in', accounts: [signedIn, secondSignedIn] }
    port.quotaResults = { 'acct-1': { accountId: 'acct-1', quota: usage } }
    const model = new CodexModel(port)
    await model.connect()
    await model.refreshQuota('acct-1')
    port.statusResult = { state: 'signed_in', accounts: [secondSignedIn] }
    const ok = await model.logout('acct-1', false)
    expect(ok).toBe(true)
    expect(port.logoutIDs).toEqual(['acct-1'])
    expect(port.logoutRemoveFlags).toEqual([false])
    expect(model.snapshot().state).toBe('signed_in')
    expect(model.snapshot().accounts).toEqual([secondSignedIn])
    // The disconnected account takes its usage row with it; nothing else moves.
    expect(model.snapshot().quotas['acct-1']).toBeUndefined()
    expect(model.snapshot().logoutError).toBe('')
    model.dispose()
  })

  it('removes every account when removal is asked', async () => {
    const port = new FakeCodexPort()
    port.statusResult = { state: 'signed_in', accounts: [signedIn] }
    const model = new CodexModel(port)
    await model.connect()
    port.statusResult = { state: 'signed_out', accounts: [] }
    const ok = await model.logout(null, true)
    expect(ok).toBe(true)
    expect(port.logoutIDs).toEqual([null])
    expect(port.logoutRemoveFlags).toEqual([true])
    expect(model.snapshot().state).toBe('signed_out')
    expect(model.snapshot().accounts).toEqual([])
    model.dispose()
  })

  it('reports a failed removal as readable text without throwing', async () => {
    const port = new FakeCodexPort()
    port.statusResult = { state: 'signed_in', accounts: [signedIn] }
    port.logoutError = new ControlPlaneError('command_failed', 'codex provider could not be removed: secure storage is unavailable')
    const model = new CodexModel(port)
    await model.connect()
    const ok = await model.logout(null, true)
    expect(ok).toBe(false)
    expect(model.snapshot().logoutError).toBe('Codex could not be removed. Try again, or disconnect it instead.')
    model.dispose()
  })

  it('reports a failed logout as readable text without throwing', async () => {
    const port = new FakeCodexPort()
    port.statusResult = { state: 'signed_in', accounts: [signedIn] }
    port.logoutError = new ControlPlaneError('timeout', 'Sidecar command timed out')
    const model = new CodexModel(port)
    await model.connect()
    const ok = await model.logout(null, false)
    expect(ok).toBe(false)
    expect(model.snapshot().logoutError).toBe('The control plane did not answer in time. Check that the relay is running, then try again.')
    model.dispose()
  })

  it('maps the active-route refusal to readable copy when a logout fails', async () => {
    const port = new FakeCodexPort()
    port.statusResult = { state: 'signed_in', accounts: [signedIn] }
    port.logoutError = new ControlPlaneError('command_failed', 'Codex is the active provider. Switch the active route away from Codex before disconnecting.')
    const model = new CodexModel(port)
    await model.connect()
    const ok = await model.logout(null, false)
    expect(ok).toBe(false)
    expect(model.snapshot().logoutError).toBe('Codex is the active provider. Switch the active route away from Codex, then disconnect.')
    model.dispose()
  })

  it('maps the active-route refusal even when the backend joins other errors to it', async () => {
    const port = new FakeCodexPort()
    port.statusResult = { state: 'signed_in', accounts: [signedIn] }
    port.logoutError = new ControlPlaneError(
      'command_failed',
      'Codex is the active provider. Switch the active route away from Codex before disconnecting.\ncodex session could not be cleared: disk full',
    )
    const model = new CodexModel(port)
    await model.connect()
    await model.logout(null, false)
    expect(model.snapshot().logoutError).toBe('Codex is the active provider. Switch the active route away from Codex, then disconnect.')
    model.dispose()
  })

  it('keeps the authorize URL for reopening while waiting', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    await model.startLogin()
    expect(model.snapshot().loginPhase).toBe('waiting')
    await model.reopenAuthorizeUrl()
    expect(port.openUrls).toEqual([port.authorizeUrl, port.authorizeUrl])
    model.dispose()
  })

  it('starts a device login: connecting, then waiting with the code, and opens nothing', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    void model.startDeviceLogin()
    expect(model.snapshot()).toMatchObject({ loginPhase: 'connecting', activeMethod: 'device' })
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot()).toMatchObject({
      loginPhase: 'waiting',
      activeMethod: 'device',
      deviceUserCode: 'WDJB-MJCD',
      deviceVerificationUrl: 'https://auth.openai.com/codex/device',
      authorizeUrl: '',
    })
    // The device flow never opens a browser on its own: the user chooses
    // where to enter the code.
    expect(port.openUrls).toEqual([])
    model.dispose()
  })

  it('refuses a browser start while a device login is waiting', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    await model.startDeviceLogin()
    expect(model.snapshot().loginPhase).toBe('waiting')
    void model.startLogin()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(port.loginStartCalls).toBe(0)
    expect(model.snapshot()).toMatchObject({ loginPhase: 'waiting', activeMethod: 'device' })
    model.dispose()
  })

  it('refuses a device start while a browser login is waiting', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    await model.startLogin()
    void model.startDeviceLogin()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(port.deviceLoginStartCalls).toBe(0)
    expect(model.snapshot()).toMatchObject({ loginPhase: 'waiting', activeMethod: 'browser' })
    model.dispose()
  })

  it('refuses imports while a browser login is live', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    await model.startLogin()
    await model.importFromJson('{}')
    expect(port.importJsonCalls).toEqual([])
    model.dispose()
  })

  it('refuses imports while a device login is live', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    await model.startDeviceLogin()
    await model.importFromFiles([])
    expect(port.importFilesCalls).toEqual([])
    model.dispose()
  })

  it('delivers device codes on the push and drops them when it ends', async () => {
    const port = new FakeCodexPort()
    port.login = { phase: 'waiting', deviceUserCode: 'ABCD-1234', deviceVerificationUrl: 'https://auth.openai.com/codex/device' }
    const model = new CodexModel(port)
    await model.connect()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot()).toMatchObject({ loginPhase: 'waiting', deviceUserCode: 'ABCD-1234' })
    port.login = { phase: 'success' }
    port.statusResult = { state: 'signed_in', accounts: [signedIn] }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot()).toMatchObject({ loginPhase: 'success', deviceUserCode: '', deviceVerificationUrl: '' })
    model.dispose()
  })

  it('maps a device flow timeout to readable copy', async () => {
    const port = new FakeCodexPort()
    port.login = { phase: 'waiting' }
    const model = new CodexModel(port)
    await model.connect()
    port.login = { phase: 'error', error: 'codex device login timed out' }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot()).toMatchObject({ loginPhase: 'error', loginError: 'The device sign-in timed out before it completed. Try again.' })
    model.dispose()
  })

  it('maps an unknown device error to the generic sign-in copy', async () => {
    const port = new FakeCodexPort()
    port.deviceLoginStartError = new ControlPlaneError('command_failed', 'codex device login start: no route to auth host')
    const model = new CodexModel(port)
    await model.connect()
    await model.startDeviceLogin()
    expect(model.snapshot()).toMatchObject({ loginPhase: 'error', loginError: 'Codex sign-in could not be started.' })
    model.dispose()
  })

  it('cancels a device login: idle, no code, no verification URL', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    await model.startDeviceLogin()
    expect(model.snapshot().deviceUserCode).toBe('WDJB-MJCD')
    await model.cancelLogin()
    expect(model.snapshot()).toMatchObject({
      loginPhase: 'idle',
      activeMethod: null,
      deviceUserCode: '',
      deviceVerificationUrl: '',
    })
    expect(port.loginCancelCalls).toBe(1)
    model.dispose()
  })

  it('aborts an in-flight device start when cancelled', async () => {
    const port = new FakeCodexPort()
    let release: () => void = () => {}
    port.deviceLoginStartDelay = new Promise<void>((resolve) => { release = () => resolve() })
    const model = new CodexModel(port)
    await model.connect()
    const pending = model.startDeviceLogin()
    expect(model.snapshot().loginPhase).toBe('connecting')
    await model.cancelLogin()
    expect(port.abortSignal?.aborted).toBe(true)
    release()
    await pending
    expect(model.snapshot().loginPhase).toBe('idle')
    model.dispose()
  })

  it('opens the verification page only on an explicit click', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    await model.openVerificationUrl()
    expect(port.openUrls).toEqual([])
    await model.startDeviceLogin()
    expect(port.openUrls).toEqual([])
    await model.openVerificationUrl()
    expect(port.openUrls).toEqual([port.deviceStart.verificationUrl])
    model.dispose()
  })

  it('imports pasted JSON: connecting then success, and the text never enters state', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    const text = '{"account_id":"acct-secret","tokens":{"refresh":"tok-secret"}}'
    const pending = model.importFromJson(text)
    expect(model.snapshot()).toMatchObject({ loginPhase: 'connecting', activeMethod: 'importJson' })
    await pending
    expect(model.snapshot()).toMatchObject({
      loginPhase: 'success',
      activeMethod: 'importJson',
      loginError: '',
      state: 'signed_in',
      importedFrom: '',
    })
    expect(model.snapshot().accounts).toEqual([signedIn])
    expect(model.snapshot().freshAccount).toEqual(signedIn)
    expect(port.importJsonCalls).toEqual([text])
    // The pasted credential text is the one thing that must not survive the call.
    expect(JSON.stringify(model.snapshot())).not.toContain('tok-secret')
    expect(JSON.stringify(model.snapshot())).not.toContain('acct-secret')
    model.dispose()
  })

  it('imports picked files: success carries the base file name', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    await model.importFromFiles(['C:\\Users\\dev\\auth.json'])
    expect(model.snapshot()).toMatchObject({ loginPhase: 'success', activeMethod: 'importFile', importedFrom: 'auth.json' })
    expect(port.importFilesCalls).toEqual([['C:\\Users\\dev\\auth.json']])
    model.dispose()
  })

  it('imports a file that holds several accounts at once', async () => {
    const port = new FakeCodexPort()
    port.importFilesResult = { state: 'signed_in', accounts: [signedIn, secondSignedIn], importedFrom: 'auth.json' }
    const model = new CodexModel(port)
    await model.connect()
    await model.importFromFiles(['C:\\Users\\dev\\auth.json'])
    expect(model.snapshot()).toMatchObject({ loginPhase: 'success', state: 'signed_in', importedFrom: 'auth.json' })
    expect(model.snapshot().accounts).toEqual([signedIn, secondSignedIn])
    expect(model.snapshot().freshAccount).toEqual(secondSignedIn)
    model.dispose()
  })

  it('a JSON import never adopts the file source field', async () => {
    const port = new FakeCodexPort()
    port.importJsonResult = { state: 'signed_in', accounts: [signedIn], importedFrom: 'leak.json' }
    const model = new CodexModel(port)
    await model.connect()
    await model.importFromJson('{"tokens":{}}')
    expect(model.snapshot().importedFrom).toBe('')
    model.dispose()
  })

  it('an import that does not sign in lands as an error', async () => {
    const port = new FakeCodexPort()
    port.importFilesResult = { state: 'signed_out', accounts: [] }
    const model = new CodexModel(port)
    await model.connect()
    await model.importFromFiles(['C:\\Users\\dev\\auth.json'])
    expect(model.snapshot()).toMatchObject({ loginPhase: 'error', loginError: 'The Codex credentials could not be imported.' })
    model.dispose()
  })

  it('maps a refresh token the sign-in server rejected to readable copy', async () => {
    const port = new FakeCodexPort()
    port.importJsonError = new ControlPlaneError(
      'command_failed',
      'codex import failed: none of the 1 credentials produced a codex session: codex refresh token was rejected: codex oauth refresh failed: refresh_token_reused',
    )
    const model = new CodexModel(port)
    await model.connect()
    await model.importFromJson('{"tokens":{}}')
    expect(model.snapshot()).toMatchObject({
      loginPhase: 'error',
      loginError: 'The Codex sign-in server refused these credentials. Export fresh credentials from Codex, then import them again.',
    })
    model.dispose()
  })

  it('maps a sign-in server that could not be reached to readable copy', async () => {
    const port = new FakeCodexPort()
    port.importJsonError = new ControlPlaneError(
      'command_failed',
      'codex import failed: none of the 1 credentials produced a codex session: codex oauth refresh failed: dial tcp 1.2.3.4:443: connect: no route to host',
    )
    const model = new CodexModel(port)
    await model.connect()
    await model.importFromJson('{"tokens":{}}')
    expect(model.snapshot()).toMatchObject({
      loginPhase: 'error',
      loginError: 'The Codex sign-in server could not be reached. Check the connection, then try again.',
    })
    model.dispose()
  })

  it('maps a file with no usable credentials to readable copy', async () => {
    const port = new FakeCodexPort()
    port.importFilesError = new ControlPlaneError('command_failed', 'none of the selected files held codex credentials')
    const model = new CodexModel(port)
    await model.connect()
    await model.importFromFiles(['C:\\Users\\dev\\notes.txt'])
    expect(model.snapshot()).toMatchObject({ loginPhase: 'error', loginError: 'None of the selected files held Codex credentials. Pick the file that was exported from Codex.' })
    model.dispose()
  })

  it('maps a payload that cannot be parsed to readable copy', async () => {
    const port = new FakeCodexPort()
    port.importJsonError = new ControlPlaneError('command_failed', 'codex import payload could not be parsed')
    const model = new CodexModel(port)
    await model.connect()
    await model.importFromJson('not json')
    expect(model.snapshot()).toMatchObject({ loginPhase: 'error', loginError: 'The Codex credentials could not be imported.' })
    model.dispose()
  })

  it('maps an unknown import error to generic copy', async () => {
    const port = new FakeCodexPort()
    port.importFilesError = new ControlPlaneError('command_failed', 'codex import: secure storage is unavailable')
    const model = new CodexModel(port)
    await model.connect()
    await model.importFromFiles(['C:\\Users\\dev\\auth.json'])
    expect(model.snapshot()).toMatchObject({ loginPhase: 'error', loginError: 'The Codex credentials could not be imported.' })
    model.dispose()
  })

  it('drops a late import result after a cancel', async () => {
    const port = new FakeCodexPort()
    let release: () => void = () => {}
    port.importDelay = new Promise<void>((resolve) => { release = () => resolve() })
    const model = new CodexModel(port)
    await model.connect()
    const pending = model.importFromJson('{}')
    await model.cancelLogin()
    release()
    await pending
    expect(model.snapshot()).toMatchObject({ loginPhase: 'idle', state: 'signed_out', freshAccount: null })
    expect(model.snapshot().accounts).toEqual([])
    model.dispose()
  })

  it('acknowledgeOutcome clears the import outcome and its source', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    await model.importFromFiles(['C:\\Users\\dev\\auth.json'])
    expect(model.snapshot()).toMatchObject({ loginPhase: 'success', activeMethod: 'importFile', importedFrom: 'auth.json' })
    expect(model.snapshot().freshAccount).toEqual(signedIn)
    model.acknowledgeOutcome()
    expect(model.snapshot()).toMatchObject({ loginPhase: 'idle', activeMethod: null, importedFrom: '', freshAccount: null })
    model.dispose()
  })

  it('settles a usage probe with a pending state that is visible while it runs', async () => {
    const port = new FakeCodexPort()
    port.statusResult = { state: 'signed_in', accounts: [signedIn] }
    port.quotaResults = { 'acct-1': { accountId: 'acct-1', quota: usage } }
    let release: () => void = () => {}
    port.quotaDelay = new Promise<void>((resolve) => { release = () => resolve() })
    const model = new CodexModel(port)
    await model.connect()
    const probing = model.refreshQuota('acct-1')
    expect(model.snapshot().quotas['acct-1']?.pending).toBe(true)
    release()
    await probing
    expect(model.snapshot().quotas['acct-1']).toMatchObject({
      pending: false,
      error: '',
      quota: {
        fetchedAt: 1_789_000_000,
        planType: 'Pro',
        primary: { remainingPercent: 78, windowMinutes: 300, resetAt: 1_789_003_120 },
        secondary: { remainingPercent: 41, windowMinutes: 10_080, resetAt: 1_789_172_800 },
      },
    })
    expect(port.quotaCalls).toEqual(['acct-1'])
    model.dispose()
  })

  it('probes each account on its own, and both cards settle', async () => {
    const port = new FakeCodexPort()
    port.statusResult = { state: 'signed_in', accounts: [signedIn, secondSignedIn] }
    port.quotaResults = {
      'acct-1': { accountId: 'acct-1', quota: usage },
      'acct-2': {
        accountId: 'acct-2',
        quota: {
          fetchedAt: 1_789_000_500,
          planType: 'Plus',
          primary: { present: true, remainingPercent: 96, windowMinutes: 300, resetAt: 1_789_003_620 },
          secondary: { present: false, remainingPercent: 100 },
        },
      },
    }
    const model = new CodexModel(port)
    await model.connect()
    await model.refreshQuota('acct-1')
    await model.refreshQuota('acct-2')
    expect(port.quotaCalls).toEqual(['acct-1', 'acct-2'])
    expect(model.snapshot().quotas['acct-1']?.quota?.primary.remainingPercent).toBe(78)
    expect(model.snapshot().quotas['acct-2']?.quota?.primary.remainingPercent).toBe(96)
    model.dispose()
  })

  it('keeps the last good windows for an account whose probe now fails', async () => {
    const port = new FakeCodexPort()
    port.statusResult = { state: 'signed_in', accounts: [signedIn, secondSignedIn] }
    port.quotaResults = {
      'acct-1': {
        accountId: 'acct-1',
        quota: {
          fetchedAt: 100,
          planType: 'Pro',
          primary: { present: true, remainingPercent: 60, windowMinutes: 300, resetAt: 400 },
          secondary: { present: false, remainingPercent: 100 },
        },
      },
    }
    const model = new CodexModel(port)
    await model.connect()
    await model.refreshQuota('acct-1')
    port.quotaResults = { 'acct-1': { accountId: 'acct-1', error: 'codex oauth usage probe failed: http 503' } }
    await model.refreshQuota('acct-1')
    expect(model.snapshot().quotas['acct-1']).toMatchObject({
      pending: false,
      error: 'The Codex usage could not be loaded.',
      quota: { fetchedAt: 100, primary: { remainingPercent: 60 } },
    })
    // An account that was never probed has no row invented for it.
    expect(model.snapshot().quotas['acct-2']).toBeUndefined()
    model.dispose()
  })

  it('maps each backend quota refusal to readable copy on the account card', async () => {
    const cases: readonly [string, string][] = [
      ['codex is not signed in', 'The account is no longer signed in. Sign in again to see its usage.'],
      ['codex oauth usage probe failed: http 503', 'The Codex usage could not be loaded.'],
      ['codex quota report is not available yet', 'The Codex usage could not be loaded.'],
    ]
    for (const [message, expected] of cases) {
      const port = new FakeCodexPort()
      port.statusResult = { state: 'signed_in', accounts: [signedIn] }
      port.quotaResults = { 'acct-1': { accountId: 'acct-1', error: message } }
      const model = new CodexModel(port)
      await model.connect()
      await model.refreshQuota('acct-1')
      expect(model.snapshot().quotas['acct-1']?.error).toBe(expected)
      expect(model.snapshot().quotas['acct-1']?.quota).toBeNull()
      model.dispose()
    }
  })

  it('refuses a same-account re-probe while it is pending, and allows a different account', async () => {
    const port = new FakeCodexPort()
    port.statusResult = { state: 'signed_in', accounts: [signedIn, secondSignedIn] }
    let release: () => void = () => {}
    port.quotaDelay = new Promise<void>((resolve) => { release = () => resolve() })
    const model = new CodexModel(port)
    await model.connect()
    const first = model.refreshQuota('acct-1')
    const second = model.refreshQuota('acct-2')
    await model.refreshQuota('acct-1')
    expect(port.quotaCalls).toEqual(['acct-1', 'acct-2'])
    expect(model.snapshot().quotas['acct-1']?.pending).toBe(true)
    release()
    await first
    await second
    expect(model.snapshot().quotas['acct-1']?.pending).toBe(false)
    model.dispose()
  })

  it('an ambient refresh leaves usage rows alone, a vanished account drops its row', async () => {
    const port = new FakeCodexPort()
    port.statusResult = { state: 'signed_in', accounts: [signedIn, secondSignedIn] }
    port.quotaResults = { 'acct-1': { accountId: 'acct-1', error: 'codex oauth usage probe failed: http 503' } }
    const model = new CodexModel(port)
    await model.connect()
    await model.refreshQuota('acct-1')
    expect(model.snapshot().quotas['acct-1']?.error).toBe('The Codex usage could not be loaded.')
    await model.refresh()
    expect(model.snapshot().quotas['acct-1']?.error).toBe('The Codex usage could not be loaded.')
    port.statusResult = { state: 'signed_in', accounts: [secondSignedIn] }
    await model.refresh()
    expect(model.snapshot().quotas['acct-1']).toBeUndefined()
    model.dispose()
  })

  it('releases the subscription on dispose', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    model.dispose()
    expect(port.listener).toBeNull()
    model.dispose()
  })
})
