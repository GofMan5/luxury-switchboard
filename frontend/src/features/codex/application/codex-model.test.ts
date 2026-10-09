import { describe, expect, it } from 'vitest'
import { ControlPlaneError } from '../../../shared/contracts/protocol'
import type { CodexAccount, CodexLoginStatus, CodexQuotaResult } from '../domain/codex'
import type { CodexDeviceLoginStart, CodexImportResult, CodexPort } from './codex-port'
import { CodexModel } from './codex-model'

const signedIn: CodexAccount = { state: 'signed_in', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex-1' }
const signedOut: CodexAccount = { state: 'signed_out', email: '', plan: '', accountId: '', providerId: '' }

class FakeCodexPort implements CodexPort {
  account: CodexAccount = signedOut
  login: CodexLoginStatus = { phase: 'idle' }
  authorizeUrl = 'https://auth.openai.com/authorize'
  loginStartCalls = 0
  loginCancelCalls = 0
  logoutCalls = 0
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

  // Usage probe: what codex.quota answers, and how it can fail.
  quotaResult: CodexQuotaResult = { ...signedOut, quota: undefined, error: 'codex is not signed in' }
  quotaError: Error | null = null
  quotaCalls = 0
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
  async status(): Promise<CodexAccount> { return this.account }

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
    return this.importJsonResult ?? { ...signedIn }
  }

  async importFiles(paths: readonly string[], signal?: AbortSignal): Promise<CodexImportResult> {
    this.importFilesCalls = [...this.importFilesCalls, [...paths]]
    this.abortSignal = signal ?? null
    await this.importDelay
    if (this.importFilesError) throw this.importFilesError
    return this.importFilesResult ?? { ...signedIn, importedFrom: 'auth.json' }
  }
  async logout(remove = false): Promise<void> {
    this.logoutCalls += 1
    this.logoutRemoveFlags.push(remove)
    if (this.logoutError) throw this.logoutError
  }

  async quota(): Promise<CodexQuotaResult> {
    this.quotaCalls += 1
    await this.quotaDelay
    if (this.quotaError) throw this.quotaError
    return this.quotaResult
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
  it('connects once and loads the account and login phase', async () => {
    const port = new FakeCodexPort()
    port.account = signedIn
    const model = new CodexModel(port)
    await model.connect()
    await model.connect()
    expect(port.listener).not.toBeNull()
    expect(model.snapshot()).toMatchObject({ loginPhase: 'idle', account: signedIn, loginError: '', authorizeUrl: '' })
    model.dispose()
  })

  it('refetches when codex.changed nudges', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    expect(model.snapshot().account).toMatchObject({ state: 'signed_out' })
    port.account = signedIn
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot().account).toMatchObject({ state: 'signed_in', providerId: 'codex-1' })
    model.dispose()
  })

  it('starts a login: connecting, then waiting once the browser opens', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    void model.startLogin()
    expect(model.snapshot().loginPhase).toBe('connecting')
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot().loginPhase).toBe('waiting')
    expect(port.openUrls).toEqual([port.authorizeUrl])
    expect(model.snapshot().authorizeUrl).toBe(port.authorizeUrl)
    model.dispose()
  })

  it('ignores a second start while one is in flight', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    void model.startLogin()
    void model.startLogin()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(port.loginStartCalls).toBe(1)
    model.dispose()
  })

  it('maps a timeout to readable copy', async () => {
    const port = new FakeCodexPort()
    port.loginStartError = new ControlPlaneError('timeout', 'Sidecar command timed out')
    const model = new CodexModel(port)
    await model.connect()
    await model.startLogin()
    expect(model.snapshot().loginPhase).toBe('error')
    expect(model.snapshot().loginError).toBe('The control plane did not answer in time. Check that the relay is running, then try again.')
    model.dispose()
  })

  it('surfaces a refused authorize URL as an error phase', async () => {
    const port = new FakeCodexPort()
    port.openAuthorizeUrlError = new ControlPlaneError('insecure_url', 'The sign-in address was not HTTPS.')
    const model = new CodexModel(port)
    await model.connect()
    await model.startLogin()
    expect(model.snapshot().loginPhase).toBe('error')
    expect(model.snapshot().loginError).toBe('The sign-in address was not HTTPS.')
    model.dispose()
  })

  it('maps a port-in-use login failure to readable copy', async () => {
    const port = new FakeCodexPort()
    port.loginStartError = new ControlPlaneError(
      'codex_login_failed',
      'codex oauth port 1455 in use: listen tcp 127.0.0.1:1455: bind: Only one usage of each socket address (protocol/network address/port) is normally permitted.',
    )
    const model = new CodexModel(port)
    await model.connect()
    await model.startLogin()
    expect(model.snapshot().loginPhase).toBe('error')
    expect(model.snapshot().loginError).toBe('The local sign-in port is already in use. Close the other sign-in attempt, then try again.')
    model.dispose()
  })

  it('falls back to generic copy for an unknown codex_login_failed message', async () => {
    const port = new FakeCodexPort()
    port.loginStartError = new ControlPlaneError('codex_login_failed', 'dial tcp: broken')
    const model = new CodexModel(port)
    await model.connect()
    await model.startLogin()
    expect(model.snapshot().loginPhase).toBe('error')
    expect(model.snapshot().loginError).toBe('Codex sign-in could not be started.')
    model.dispose()
  })

  it('does not map a port-shaped message that lacks the login-failed code', async () => {
    const port = new FakeCodexPort()
    port.loginStartError = new ControlPlaneError('command_failed', 'codex oauth port 1455 in use: listen tcp 127.0.0.1:1455: bind: address already in use')
    const model = new CodexModel(port)
    await model.connect()
    await model.startLogin()
    expect(model.snapshot().loginError).toBe('Codex sign-in could not be started.')
    model.dispose()
  })

  it('maps a timed-out login pushed as the backend phase error', async () => {
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

  it('maps a cancelled login pushed as the backend phase error', async () => {
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

  it('falls back to generic copy for a near-miss phase error string', async () => {
    const port = new FakeCodexPort()
    port.login = { phase: 'waiting' }
    const model = new CodexModel(port)
    await model.connect()
    port.login = { phase: 'error', error: 'codex login timed out badly' }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot()).toMatchObject({
      loginPhase: 'error',
      loginError: 'Codex sign-in could not be started.',
    })
    model.dispose()
  })

  it('maps an unstored session pushed as the backend phase error', async () => {
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

  it('cancels: sets idle, clears the error and notifies the backend', async () => {
    const port = new FakeCodexPort()
    port.login = { phase: 'waiting' }
    const model = new CodexModel(port)
    await model.connect()
    await model.cancelLogin()
    expect(model.snapshot().loginPhase).toBe('idle')
    expect(port.loginCancelCalls).toBe(1)
    model.dispose()
  })

  it('aborts an in-flight start when cancelled', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    const pending = model.startLogin()
    await model.cancelLogin()
    expect(port.abortSignal?.aborted).toBe(true)
    await pending
    expect(model.snapshot().loginPhase).toBe('idle')
    model.dispose()
  })

  it('keeps the authorize URL when the backend pushes waiting before login.start returns', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    let release: () => void = () => {}
    port.loginStartDelay = new Promise<void>((resolve) => { release = () => resolve() })
    const pending = model.startLogin()
    expect(model.snapshot().loginPhase).toBe('connecting')
    // The backend fires codex.changed(waiting) while login.start is still in
    // flight; the phase moves, but this attempt has no URL yet.
    port.login = { phase: 'waiting' }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot()).toMatchObject({ loginPhase: 'waiting', authorizeUrl: '' })
    expect(port.openUrls).toEqual([])
    release()
    await pending
    // The resolved attempt must still get its URL and open the browser.
    expect(model.snapshot().loginPhase).toBe('waiting')
    expect(model.snapshot().authorizeUrl).toBe(port.authorizeUrl)
    expect(port.openUrls).toEqual([port.authorizeUrl])
    model.dispose()
  })

  it('keeps a cancelled login idle when its completion is pushed late', async () => {
    const port = new FakeCodexPort()
    port.login = { phase: 'waiting' }
    const model = new CodexModel(port)
    await model.connect()
    expect(model.snapshot().loginPhase).toBe('waiting')
    await model.cancelLogin()
    expect(model.snapshot().loginPhase).toBe('idle')
    // The backend's cancel is advisory: the exchange can still complete and
    // push exchanging/success after the user watched the flow close.
    port.login = { phase: 'exchanging' }
    port.account = signedIn
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot()).toMatchObject({ loginPhase: 'idle', loginError: '', authorizeUrl: '' })
    expect(model.snapshot().account).toMatchObject({ state: 'signed_in' })
    port.login = { phase: 'success' }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot().loginPhase).toBe('idle')
    model.dispose()
  })

  it('merges backend phases again after a fresh startLogin', async () => {
    const port = new FakeCodexPort()
    port.login = { phase: 'waiting' }
    const model = new CodexModel(port)
    await model.connect()
    await model.cancelLogin()
    expect(model.snapshot().loginPhase).toBe('idle')
    await model.startLogin()
    expect(model.snapshot().loginPhase).toBe('waiting')
    port.login = { phase: 'exchanging' }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot().loginPhase).toBe('exchanging')
    model.dispose()
  })

  it('treats the backend phase as truth once a login is pending', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    port.login = { phase: 'exchanging' }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot().loginPhase).toBe('exchanging')
    port.login = { phase: 'error', error: 'Port 1455 is busy' }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot()).toMatchObject({ loginPhase: 'error', loginError: 'Codex sign-in could not be started.' })
    model.dispose()
  })

  it('settles to idle when the backend reports idle after a restart', async () => {
    const port = new FakeCodexPort()
    port.login = { phase: 'waiting' }
    const model = new CodexModel(port)
    await model.connect()
    expect(model.snapshot().loginPhase).toBe('waiting')
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
    expect(model.snapshot()).toMatchObject({ loginPhase: 'error', loginError: 'Codex sign-in could not be started.' })
    port.login = { phase: 'idle' }
    port.account = signedIn
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot()).toMatchObject({ loginPhase: 'error', loginError: 'Codex sign-in could not be started.' })
    expect(model.snapshot().account).toMatchObject({ state: 'signed_in' })
    model.dispose()
  })

  it('narrows sticky success to a still-signed-in account', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    port.login = { phase: 'success' }
    port.account = signedIn
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot().loginPhase).toBe('success')
    // The linked session expired immediately: success must not stay announced.
    port.login = { phase: 'idle' }
    port.account = { ...signedIn, state: 'reauth_needed' }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot().loginPhase).toBe('idle')
    model.dispose()
  })

  it('acknowledgeOutcome resets a terminal phase and drops the held URL', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    port.login = { phase: 'success' }
    port.account = signedIn
    await model.refresh()
    expect(model.snapshot().loginPhase).toBe('success')
    model.acknowledgeOutcome()
    expect(model.snapshot()).toMatchObject({ loginPhase: 'idle', loginError: '' })
    model.acknowledgeOutcome() // no-op once idle
    expect(model.snapshot().loginPhase).toBe('idle')
    model.dispose()
  })

  it('acknowledgeOutcome ignores in-flight phases', async () => {
    const port = new FakeCodexPort()
    port.login = { phase: 'exchanging' }
    const model = new CodexModel(port)
    await model.connect()
    model.acknowledgeOutcome()
    expect(model.snapshot().loginPhase).toBe('exchanging')
    model.dispose()
  })

  it('logs out and refreshes the account', async () => {
    const port = new FakeCodexPort()
    port.account = signedIn
    const model = new CodexModel(port)
    await model.connect()
    port.account = signedOut
    const ok = await model.logout()
    expect(ok).toBe(true)
    expect(port.logoutCalls).toBe(1)
    expect(model.snapshot().account).toMatchObject({ state: 'signed_out' })
    expect(model.snapshot().logoutError).toBe('')
    model.dispose()
  })

  it('removes the provider entry when the logout is asked to', async () => {
    const port = new FakeCodexPort()
    port.account = signedIn
    const model = new CodexModel(port)
    await model.connect()
    port.account = signedOut
    const ok = await model.logout(true)
    expect(ok).toBe(true)
    // The delete and the disconnect are the same command with one optional
    // flag; the model must forward which of the two the user chose.
    expect(port.logoutRemoveFlags).toEqual([true])
    expect(model.snapshot().account).toMatchObject({ state: 'signed_out' })
    expect(model.snapshot().logoutError).toBe('')
    model.dispose()
  })

  it('reports a failed removal with its own readable copy', async () => {
    const port = new FakeCodexPort()
    port.logoutError = new ControlPlaneError('command_failed', 'codex provider could not be removed: secure storage is unavailable')
    const model = new CodexModel(port)
    await model.connect()
    const ok = await model.logout(true)
    expect(ok).toBe(false)
    expect(model.snapshot().logoutError).toBe('Codex could not be removed. Try again, or disconnect it instead.')
    model.dispose()
  })

  it('reports a failed logout as readable text without throwing', async () => {
    const port = new FakeCodexPort()
    port.logoutError = new ControlPlaneError('timeout', 'Sidecar command timed out')
    const model = new CodexModel(port)
    await model.connect()
    const ok = await model.logout()
    expect(ok).toBe(false)
    expect(model.snapshot().logoutError).toBe('The control plane did not answer in time. Check that the relay is running, then try again.')
    model.dispose()
  })

  it('maps the active-route refusal to readable copy when a logout fails', async () => {
    const port = new FakeCodexPort()
    port.logoutError = new ControlPlaneError('command_failed', 'Codex is the active provider. Switch the active route away from Codex before disconnecting.')
    const model = new CodexModel(port)
    await model.connect()
    const ok = await model.logout()
    expect(ok).toBe(false)
    expect(model.snapshot().logoutError).toBe('Codex is the active provider. Switch the active route away from Codex, then disconnect.')
    model.dispose()
  })

  it('maps the active-route refusal even when the backend joins other errors to it', async () => {
    const port = new FakeCodexPort()
    port.logoutError = new ControlPlaneError(
      'command_failed',
      'Codex is the active provider. Switch the active route away from Codex before disconnecting.\ncodex session could not be cleared: disk full',
    )
    const model = new CodexModel(port)
    await model.connect()
    await model.logout()
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
    expect(model.snapshot().loginPhase).toBe('waiting')
    void model.startDeviceLogin()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(port.deviceLoginStartCalls).toBe(0)
    expect(model.snapshot()).toMatchObject({ loginPhase: 'waiting', activeMethod: 'browser' })
    model.dispose()
  })

  it('refuses imports while a login is live, in both directions', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    await model.startDeviceLogin()
    void model.importFromJson('{"tokens":{}}')
    void model.importFromFiles(['C:\\Users\\dev\\auth.json'])
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(port.importJsonCalls).toEqual([])
    expect(port.importFilesCalls).toEqual([])
    expect(model.snapshot().loginPhase).toBe('waiting')

    // And an import in flight refuses a device start the same way.
    const port2 = new FakeCodexPort()
    const model2 = new CodexModel(port2)
    await model2.connect()
    let release: () => void = () => {}
    port2.importDelay = new Promise<void>((resolve) => { release = () => resolve() })
    void model2.importFromJson('{"tokens":{}}')
    expect(model2.snapshot()).toMatchObject({ loginPhase: 'connecting', activeMethod: 'importJson' })
    void model2.startDeviceLogin()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(port2.deviceLoginStartCalls).toBe(0)
    release()
    await new Promise((resolve) => setTimeout(resolve, 0))
    model2.dispose()
    model.dispose()
  })

  it('delivers device codes from backend pushes and drops them when the flow ends', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    port.login = { phase: 'waiting', deviceUserCode: 'ABCD-1234', deviceVerificationUrl: 'https://auth.openai.com/codex/device' }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot()).toMatchObject({
      loginPhase: 'waiting',
      deviceUserCode: 'ABCD-1234',
      deviceVerificationUrl: 'https://auth.openai.com/codex/device',
    })
    // The flow completing drops the code: it has no life of its own.
    port.login = { phase: 'success' }
    port.account = signedIn
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot()).toMatchObject({ loginPhase: 'success', deviceUserCode: '', deviceVerificationUrl: '' })
    model.dispose()
  })

  it('maps a device timeout pushed as the backend phase error', async () => {
    const port = new FakeCodexPort()
    port.login = { phase: 'waiting', deviceUserCode: 'WDJB-MJCD' }
    const model = new CodexModel(port)
    await model.connect()
    expect(model.snapshot().loginPhase).toBe('waiting')
    port.login = { phase: 'error', error: 'codex device login timed out' }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot()).toMatchObject({
      loginPhase: 'error',
      loginError: 'The sign-in timed out before the browser answered. Try again.',
    })
    model.dispose()
  })

  it('maps the backend busy refusal to readable copy', async () => {
    const port = new FakeCodexPort()
    port.login = { phase: 'waiting' }
    const model = new CodexModel(port)
    await model.connect()
    port.login = { phase: 'error', error: 'codex login is already in progress' }
    port.listener?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(model.snapshot()).toMatchObject({
      loginPhase: 'error',
      loginError: 'A sign-in is already in progress. Cancel it and try again.',
    })
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
      account: { state: 'signed_in', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex-1' },
      importedFrom: '',
    })
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

  it('a JSON import never adopts the file source field', async () => {
    const port = new FakeCodexPort()
    port.importJsonResult = { ...signedIn, importedFrom: 'leak.json' }
    const model = new CodexModel(port)
    await model.connect()
    await model.importFromJson('{"tokens":{}}')
    expect(model.snapshot().importedFrom).toBe('')
    model.dispose()
  })

  it('an import that does not sign in lands as an error', async () => {
    const port = new FakeCodexPort()
    port.importFilesResult = { state: 'signed_out', email: '', plan: '', accountId: '', providerId: '' }
    const model = new CodexModel(port)
    await model.connect()
    await model.importFromFiles(['C:\\Users\\dev\\auth.json'])
    expect(model.snapshot()).toMatchObject({ loginPhase: 'error', loginError: 'The Codex credentials could not be imported.' })
    model.dispose()
  })

  it('maps a rejected refresh token to readable copy', async () => {
    const port = new FakeCodexPort()
    port.importJsonError = new ControlPlaneError('command_failed', 'auth.json: the refresh token was rejected upstream')
    const model = new CodexModel(port)
    await model.connect()
    await model.importFromJson('{"tokens":{}}')
    expect(model.snapshot()).toMatchObject({ loginPhase: 'error', loginError: 'The refresh token was rejected.' })
    model.dispose()
  })

  it('maps a no-usable-files refusal to readable copy', async () => {
    const port = new FakeCodexPort()
    port.importFilesError = new ControlPlaneError('command_failed', 'none of the selected files held codex credentials')
    const model = new CodexModel(port)
    await model.connect()
    await model.importFromFiles(['C:\\Users\\dev\\auth.json'])
    expect(model.snapshot()).toMatchObject({ loginPhase: 'error', loginError: 'None of the selected files contained usable Codex credentials.' })
    model.dispose()
  })

  it('maps a multi-account import to readable copy', async () => {
    const port = new FakeCodexPort()
    port.importJsonError = new ControlPlaneError('command_failed', 'auth.json: found 2 accounts, only one is supported')
    const model = new CodexModel(port)
    await model.connect()
    await model.importFromJson('{"accounts":[]}')
    expect(model.snapshot()).toMatchObject({ loginPhase: 'error', loginError: 'The import contained multiple accounts; only one is supported.' })
    model.dispose()
  })

  it('maps an empty-credentials import to readable copy', async () => {
    const port = new FakeCodexPort()
    port.importJsonError = new ControlPlaneError('command_failed', 'no codex credentials found on line 3')
    const model = new CodexModel(port)
    await model.connect()
    await model.importFromJson('notes')
    expect(model.snapshot()).toMatchObject({ loginPhase: 'error', loginError: 'No Codex credentials were found in the input.' })
    model.dispose()
  })

  it('falls back to generic import copy for an unknown failure', async () => {
    const port = new FakeCodexPort()
    port.importJsonError = new ControlPlaneError('command_failed', 'parse boom')
    const model = new CodexModel(port)
    await model.connect()
    await model.importFromJson('notes')
    expect(model.snapshot()).toMatchObject({ loginPhase: 'error', loginError: 'The Codex credentials could not be imported.' })
    model.dispose()
  })

  it('drops a late import result after a cancel', async () => {
    const port = new FakeCodexPort()
    let release: () => void = () => {}
    port.importDelay = new Promise<void>((resolve) => { release = () => resolve() })
    const model = new CodexModel(port)
    await model.connect()
    const pending = model.importFromJson('{"tokens":{}}')
    await model.cancelLogin()
    expect(model.snapshot().loginPhase).toBe('idle')
    release()
    await pending
    // The user watched the flow close; the resolved import must not sign the
    // pane back in behind their back.
    expect(model.snapshot()).toMatchObject({ loginPhase: 'idle', loginError: '' })
    expect(model.snapshot().account).toMatchObject({ state: 'signed_out' })
    model.dispose()
  })

  it('acknowledge clears the import outcome and its source name', async () => {
    const port = new FakeCodexPort()
    const model = new CodexModel(port)
    await model.connect()
    await model.importFromFiles(['C:\\Users\\dev\\auth.json'])
    expect(model.snapshot()).toMatchObject({ loginPhase: 'success', importedFrom: 'auth.json' })
    model.acknowledgeOutcome()
    expect(model.snapshot()).toMatchObject({ loginPhase: 'idle', activeMethod: null, importedFrom: '' })
    model.dispose()
  })

  it('settles a successful usage probe, with the pend visible while it flies', async () => {
    const port = new FakeCodexPort()
    port.account = signedIn
    port.quotaResult = {
      state: 'signed_in', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex-1',
      quota: {
        fetchedAt: 1_789_000_000, planType: 'Pro',
        primary: { present: true, remainingPercent: 78, windowMinutes: 300, resetAt: 1_789_003_120 },
        secondary: { present: true, remainingPercent: 41, windowMinutes: 10_080, resetAt: 1_789_172_800 },
      },
    }
    let release!: (value: void) => void
    port.quotaDelay = new Promise((resolve) => { release = resolve })
    const model = new CodexModel(port)
    await model.connect()
    const probing = model.refreshQuota()
    expect(model.snapshot().quotaPending).toBe(true)
    release()
    await probing
    expect(model.snapshot()).toMatchObject({
      quotaPending: false,
      quotaError: '',
      quota: { fetchedAt: 1_789_000_000, planType: 'Pro', primary: { remainingPercent: 78, windowMinutes: 300 } },
    })
    model.dispose()
  })

  it('keeps the last good windows beside a failed probe, instead of wiping the card', async () => {
    const port = new FakeCodexPort()
    port.account = signedIn
    port.quotaResult = {
      state: 'signed_in', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex-1',
      quota: { fetchedAt: 100, planType: 'Pro', primary: { present: true, remainingPercent: 60, windowMinutes: 300, resetAt: 400 }, secondary: { present: false, remainingPercent: 100 } },
    }
    const model = new CodexModel(port)
    await model.connect()
    await model.refreshQuota()
    port.quotaResult = {
      state: 'signed_in', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex-1',
      error: 'codex oauth usage probe failed: http 503',
    }
    await model.refreshQuota()
    expect(model.snapshot()).toMatchObject({
      quotaPending: false,
      quotaError: 'The Codex usage could not be loaded.',
      quota: { fetchedAt: 100, primary: { remainingPercent: 60 } },
    })
    model.dispose()
  })

  it('maps each backend quota refusal to the sentence the card shows', async () => {
    const cases: readonly (readonly [string, string])[] = [
      ['codex is not signed in', 'Codex is no longer signed in. Sign in again from the Providers page.'],
      ['codex session needs sign-in', 'The Codex session expired. Sign in again from the Providers page.'],
      ['codex oauth usage probe failed: http 401: codex usage probe was unauthorized', 'The account rejected the usage request. Sign in again from the Providers page.'],
      ['codex oauth usage probe failed: context deadline exceeded', 'The usage request timed out. Try again.'],
    ]
    for (const [message, copy] of cases) {
      const port = new FakeCodexPort()
      port.account = signedIn
      port.quotaResult = { state: 'signed_in', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex-1', error: message }
      const model = new CodexModel(port)
      await model.connect()
      await model.refreshQuota()
      expect(model.snapshot().quotaError, `copy for ${message}`).toBe(copy)
      expect(model.snapshot().quota).toBeNull()
      model.dispose()
    }
  })

  it('refuses a second probe while one is already in flight', async () => {
    const port = new FakeCodexPort()
    let release!: (value: void) => void
    port.quotaDelay = new Promise((resolve) => { release = resolve })
    const model = new CodexModel(port)
    await model.connect()
    const probing = model.refreshQuota()
    await model.refreshQuota()
    release()
    await probing
    expect(port.quotaCalls).toBe(1)
    expect(model.snapshot().quotaPending).toBe(false)
    model.dispose()
  })

  it('an ambient refresh and a logout leave the usage card untouched', async () => {
    const port = new FakeCodexPort()
    port.account = signedIn
    port.quotaResult = {
      state: 'signed_in', email: 'dev@example.com', plan: 'Pro', accountId: 'acct-1', providerId: 'codex-1',
      error: 'codex oauth usage probe failed: http 503',
    }
    const model = new CodexModel(port)
    await model.connect()
    await model.refreshQuota()
    expect(model.snapshot().quotaError).toBe('The Codex usage could not be loaded.')
    await model.refresh()
    port.account = signedOut
    await model.logout()
    expect(model.snapshot()).toMatchObject({ quotaError: 'The Codex usage could not be loaded.', quota: null })
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
