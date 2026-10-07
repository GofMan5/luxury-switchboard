import { CheckCircle2, ClipboardPaste, FileUp, Globe, LogIn, MonitorSmartphone, Package, RefreshCw } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { useRef, useState } from 'react'
import type { ChangeEvent, KeyboardEvent } from 'react'
import { pickAuthFiles } from '../../../platform/lifecycle/pick-auth-files'
import { Button } from '../../../shared/ui/Button'
import { Pill } from '../../../shared/ui/chrome'
import type { CodexAuthMethod } from '../application/codex-model'
import { useCodex } from './useCodex'
import styles from './CodexConnectPane.module.css'

/** One selectable sign-in method, mirroring the cockpit's four Codex ways in. */
interface MethodEntry {
  readonly id: CodexAuthMethod
  readonly label: string
  readonly description: string
  readonly Icon: LucideIcon
}

const METHODS: readonly MethodEntry[] = [
  { id: 'browser', label: 'Official sign-in', description: 'ChatGPT opens in your default browser.', Icon: Globe },
  { id: 'device', label: 'Device code', description: 'Enter a code on the ChatGPT device page.', Icon: MonitorSmartphone },
  { id: 'importJson', label: 'Import JSON', description: 'Paste exported Codex credentials.', Icon: ClipboardPaste },
  { id: 'importFile', label: 'Import from file', description: 'Pick an exported sign-in file.', Icon: FileUp },
]

/**
 * The Codex step of Add provider: one card that stays mounted while the
 * login flows — the phase region inside the card narrates the hand-off to
 * the browser, the device page or the import. Which card is shown is frozen
 * for the whole flow: a login that started with no provider keeps the
 * connect card until its outcome is acknowledged, and a still-linked account
 * keeps the manage card even if its provider row disappeared from the list
 * meanwhile.
 */
export default function CodexConnectPane({ providerExists, onDisconnect, onSelectCodexRow }: {
  readonly providerExists: boolean
  readonly onDisconnect: () => void
  readonly onSelectCodexRow: () => void
}) {
  const { model, state } = useCodex()
  const { loginPhase, loginError, account, authorizeUrl, activeMethod, deviceUserCode, deviceVerificationUrl, importedFrom } = state

  // The pane-local method pick; the running flow pins its own method via
  // state.activeMethod, so the radios keep showing what is actually running.
  const [method, setMethod] = useState<CodexAuthMethod>('browser')
  const [importText, setImportText] = useState('')
  const [importFileError, setImportFileError] = useState('')
  const [copied, setCopied] = useState(false)
  const cardRefs = useRef<(HTMLButtonElement | null)[]>([])
  const fileInputRef = useRef<HTMLInputElement | null>(null)

  // Sampled only while no flow is live, so live phases keep rendering the
  // card the user started from instead of whatever a mid-flight push says.
  const providerBeforeLogin = useRef(providerExists)
  if (loginPhase === 'idle' || loginPhase === 'error') providerBeforeLogin.current = providerExists
  const flowLive = loginPhase === 'connecting' || loginPhase === 'waiting' || loginPhase === 'exchanging' || loginPhase === 'success'
  const manageCard = !(flowLive && !providerBeforeLogin.current) && (providerExists || account.state !== 'signed_out')
  const signedOut = account.state !== 'signed_in'

  // While a flow is live the pinned method decides which radio reads as
  // checked and which body the phase region narrates; the pane-local pick
  // takes over again once the outcome is acknowledged or cancelled.
  const effectiveMethod: CodexAuthMethod = loginPhase === 'idle' || activeMethod === null ? method : activeMethod

  const selectMethod = (next: CodexAuthMethod): void => {
    if (flowLive) return
    // Switching away retires a shown failure so the pane starts clean.
    if (loginPhase === 'error') model.acknowledgeOutcome()
    setMethod(next)
  }

  const onMethodsKeyDown = (event: KeyboardEvent<HTMLDivElement>): void => {
    if (flowLive) return
    const index = METHODS.findIndex((entry) => entry.id === method)
    let next: number
    switch (event.key) {
      case 'ArrowDown':
      case 'ArrowRight':
        event.preventDefault()
        next = (index + 1) % METHODS.length
        break
      case 'ArrowUp':
      case 'ArrowLeft':
        event.preventDefault()
        next = (index - 1 + METHODS.length) % METHODS.length
        break
      case 'Home':
        event.preventDefault()
        next = 0
        break
      case 'End':
        event.preventDefault()
        next = METHODS.length - 1
        break
      default:
        return
    }
    selectMethod(METHODS[next].id)
    cardRefs.current[next]?.focus()
  }

  const copyDeviceCode = async (): Promise<void> => {
    if (deviceUserCode === '') return
    try {
      await navigator.clipboard.writeText(deviceUserCode)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 2000)
    } catch {
      // The code stays on screen and is selectable in one click; the copy is best effort.
    }
  }

  const pickImportFile = async (): Promise<void> => {
    setImportFileError('')
    const paths = await pickAuthFiles()
    if (paths === null) {
      // No desktop shell (the dev fixture or a plain browser): the same
      // method works through a plain HTML file input.
      fileInputRef.current?.click()
      return
    }
    if (paths.length === 0) return // the native picker was dismissed
    void model.importFromFiles(paths)
  }

  const onFallbackFileChosen = async (event: ChangeEvent<HTMLInputElement>): Promise<void> => {
    const file = event.target.files?.[0]
    event.target.value = '' // the same file must stay pickable afterwards
    if (!file) return
    try {
      void model.importFromJson(await file.text())
    } catch {
      setImportFileError('The file could not be read.')
    }
  }

  const ConnectingIcon = effectiveMethod === 'browser' ? LogIn : effectiveMethod === 'device' ? MonitorSmartphone : effectiveMethod === 'importFile' ? FileUp : ClipboardPaste
  const connectingLabel = effectiveMethod === 'browser' ? 'Opening browser…' : effectiveMethod === 'device' ? 'Requesting a device code…' : 'Importing credentials…'

  return (
    <div className={styles.card}>
      <div className={styles.cardHead}>
        <Package size={20} strokeWidth={1.6} aria-hidden="true" />
        <div className={styles.cardText}>
          <strong>Codex</strong>
          {manageCard ? null : (
            <small>Uses the ChatGPT account you sign in with. Endpoint, dialect and limits come from the preset.</small>
          )}
        </div>
        {manageCard ? (
          account.state === 'signed_in' ? <Pill tone="success">Signed in</Pill>
            : account.state === 'reauth_needed' ? <Pill tone="warning">Sign-in needed</Pill>
              : <Pill>Signed out</Pill>
        ) : null}
      </div>
      {manageCard && account.state === 'reauth_needed' ? (
        <p className={styles.reauthNote}>The session expired. Sign in again to keep the provider working.</p>
      ) : null}
      {manageCard && (account.email !== '' || account.plan !== '') ? (
        <dl className={styles.rows}>
          {account.email !== '' ? <><dt>Email</dt><dd className={styles.email} title={account.email}>{account.email}</dd></> : null}
          {account.plan !== '' ? <><dt>Plan</dt><dd className={styles.email} title={account.plan}>{account.plan}</dd></> : null}
        </dl>
      ) : null}
      {manageCard ? (
        <div className={styles.cardActions}>
          <Button variant="secondary" onClick={onSelectCodexRow}>View in list</Button>
          <Button variant="danger" onClick={onDisconnect}>Disconnect</Button>
        </div>
      ) : null}
      {loginPhase !== 'idle' || !manageCard || signedOut ? (
        <div className={styles.phase}>
          {loginPhase === 'idle' || loginPhase === 'error' ? (
            <>
              {loginPhase === 'error' ? <p className={styles.error} role="alert">{loginError}</p> : null}
              <div className={styles.methods} role="radiogroup" aria-label="Codex sign-in method" onKeyDown={onMethodsKeyDown}>
                {METHODS.map((entry, index) => {
                  const selected = entry.id === effectiveMethod
                  return (
                    <button
                      key={entry.id}
                      ref={(node) => { cardRefs.current[index] = node }}
                      type="button"
                      role="radio"
                      aria-checked={selected}
                      tabIndex={selected ? 0 : -1}
                      className={styles.methodCard}
                      data-checked={selected ? 'true' : undefined}
                      disabled={flowLive}
                      onClick={() => selectMethod(entry.id)}
                    >
                      <entry.Icon size={18} strokeWidth={1.6} aria-hidden="true" />
                      <span className={styles.methodText}>
                        <strong>{entry.label}</strong>
                        <small>{entry.description}</small>
                      </span>
                    </button>
                  )
                })}
              </div>
              {effectiveMethod === 'browser' ? (
                <>
                  <Button variant="primary" onClick={() => void model.startLogin()}>
                    <LogIn size={15} aria-hidden="true" />Sign in with ChatGPT
                  </Button>
                  <small className={styles.hint}>Opens your default browser. Nothing is sent until you approve the sign-in.</small>
                </>
              ) : null}
              {effectiveMethod === 'device' ? (
                <>
                  <Button variant="primary" onClick={() => void model.startDeviceLogin()}>
                    <MonitorSmartphone size={15} aria-hidden="true" />Get a device code
                  </Button>
                  <small className={styles.hint}>You get a short code to enter on the ChatGPT device page. Nothing is sent until you approve the sign-in there.</small>
                </>
              ) : null}
              {effectiveMethod === 'importJson' ? (
                <>
                  <textarea
                    className={styles.importTextarea}
                    rows={4}
                    value={importText}
                    onChange={(event) => setImportText(event.target.value)}
                    placeholder="Paste an auth.json, an accounts export or a token…"
                    aria-label="Codex credentials"
                    spellCheck={false}
                    autoComplete="off"
                  />
                  <Button variant="primary" disabled={importText.trim() === ''} onClick={() => void model.importFromJson(importText)}>
                    <ClipboardPaste size={15} aria-hidden="true" />Import credentials
                  </Button>
                  <small className={styles.hint}>Accepts an auth.json, an accounts export, or bare tokens.</small>
                </>
              ) : null}
              {effectiveMethod === 'importFile' ? (
                <>
                  <Button variant="primary" onClick={() => void pickImportFile()}>
                    <FileUp size={15} aria-hidden="true" />Choose file…
                  </Button>
                  <small className={styles.hint}>Accepts exported Codex sign-in files (.json, .txt, .auth).</small>
                  {importFileError !== '' ? <p className={styles.error} role="alert">{importFileError}</p> : null}
                  <input
                    ref={fileInputRef}
                    type="file"
                    accept=".json,.txt,.auth"
                    className={styles.hiddenFileInput}
                    onChange={(event) => void onFallbackFileChosen(event)}
                  />
                </>
              ) : null}
            </>
          ) : null}
          {loginPhase === 'connecting' ? (
            <Button variant="primary" disabled>
              <ConnectingIcon size={15} className={styles.spinning} aria-hidden="true" />{connectingLabel}
            </Button>
          ) : null}
          {loginPhase === 'waiting' && effectiveMethod === 'browser' ? (
            <>
              <p className={styles.statusRow} role="status">
                <RefreshCw size={16} className={styles.spinning} aria-hidden="true" />Waiting for sign-in…
              </p>
              <small className={styles.hint}>Approve the sign-in in the browser window that opened, then return here.</small>
              {authorizeUrl !== '' ? (
                <Button variant="secondary" onClick={() => void model.reopenAuthorizeUrl()}>Open the sign-in page again</Button>
              ) : null}
            </>
          ) : null}
          {loginPhase === 'waiting' && effectiveMethod === 'device' ? (
            <>
              <p className={styles.statusRow} role="status">
                <RefreshCw size={16} className={styles.spinning} aria-hidden="true" />Waiting for approval…
              </p>
              {deviceUserCode !== '' ? (
                <div className={styles.deviceCode}>
                  <code className={styles.userCode} title={deviceUserCode}>{deviceUserCode}</code>
                  <Button variant="secondary" onClick={() => void copyDeviceCode()}>{copied ? 'Copied' : 'Copy code'}</Button>
                </div>
              ) : null}
              {deviceVerificationUrl !== '' ? (
                <Button variant="secondary" onClick={() => void model.openVerificationUrl()}>Open the verification page</Button>
              ) : null}
              <small className={styles.hint}>Enter the code on the ChatGPT device page and approve the sign-in. This dialog keeps watching until it finishes.</small>
            </>
          ) : null}
          {loginPhase === 'exchanging' ? (
            <>
              <p className={styles.statusRow} role="status">
                <RefreshCw size={16} className={styles.spinning} aria-hidden="true" />Finishing sign-in…
              </p>
              <small className={styles.hint}>The account is being linked and the provider is being created.</small>
            </>
          ) : null}
          {loginPhase === 'success' ? (
            <>
              <p className={styles.statusRow}>
                <CheckCircle2 size={16} className={styles.successIcon} aria-hidden="true" />
                <span>Signed in as <span className={styles.email} title={account.email}>{account.email}</span></span>
                {account.plan !== '' ? <Pill tone="info">{account.plan}</Pill> : null}
              </p>
              {importedFrom !== '' ? (
                <small className={styles.hint}>Imported from {importedFrom}.</small>
              ) : null}
              {!providerBeforeLogin.current ? (
                <small className={styles.hint}>The Codex provider was added to the list and is enabled.</small>
              ) : null}
            </>
          ) : null}
        </div>
      ) : null}
    </div>
  )
}
