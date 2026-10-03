import { useEffect, useMemo, useState, type FormEvent, type ReactElement } from 'react'
import { Activity, ArrowDown, ArrowUp, ClipboardPaste, KeyRound, Pencil, Plus, RefreshCcw, Trash2, X } from 'lucide-react'
import { useProviders } from '../../providers/ui/useProviders'
import { Button } from '../../../shared/ui/Button'
import { Pill } from '../../../shared/ui/chrome'
import { useModalFocus } from '../../../shared/ui/useModalFocus'
import type { AddApiKey, ApiKey, ImportApiKeys, ImportApiKeysReport, UpdateApiKey } from '../domain/api-key'
import { useApiKeys } from './useApiKeys'
import { parsePastedKeys, proxyURLIsValid, MAX_IMPORT_KEYS, MAX_IMPORT_FRAME_CHARS, type PastedKey } from './key-form'
import styles from './ApiKeysPage.module.css'

export default function ApiKeysPage() {
  const { state: providers } = useProviders()
  const { model, state } = useApiKeys()
  const [providerID, setProviderID] = useState('')
  const [editor, setEditor] = useState<{ mode: 'add' | 'edit'; key?: ApiKey } | null>(null)
  const [importing, setImporting] = useState(false)
  const [removeKey, setRemoveKey] = useState<ApiKey | null>(null)
  const selectedProvider = (providers.catalog.providers.some((provider) => provider.id === providerID) ? providerID : '') || providers.catalog.activeId || providers.catalog.providers[0]?.id || ''
  // The scheduler counts a key's own limit over its provider's window, so a
  // per-second provider makes every number on this page a per-second number.
  const perSecond = providers.catalog.providers.find((provider) => provider.id === selectedProvider)?.rateUnit === 'second'
  const unit = perSecond ? 'second' : 'minute'

  useEffect(() => {
    if (selectedProvider) void model.load(selectedProvider)
  }, [model, selectedProvider])

  const ordered = useMemo(
    () => [...state.keys].sort((left, right) => left.priority - right.priority),
    [state.keys],
  )
  const rejectedCount = useMemo(() => state.keys.filter((key) => key.authStreak >= 3 && !key.pinned).length, [state.keys])
  const movablePositions = useMemo(
    () => new Map(ordered.filter((key) => !key.pinned).map((key, index) => [key.id, index])),
    [ordered],
  )

  return (
    <section className={styles.page}>
      <header className="page-header">
        <div>
          <h1>API Keys</h1>
          <p>Priority, rate limits, proxy route and cooldowns</p>
        </div>
        <div className={styles.headerActions}>
          <label className={styles.providerSelect}>
            <span>Provider</span>
            <select value={selectedProvider} onChange={(event) => setProviderID(event.currentTarget.value)}>
              {providers.catalog.providers.map((provider) => <option key={provider.id} value={provider.id}>{provider.name}</option>)}
            </select>
          </label>
          <Button disabled={!selectedProvider || state.checkingPool} onClick={() => void model.checkPool()}>
            <Activity size={16} aria-hidden="true" className={state.checkingPool ? styles.spinning : undefined} />
            {state.checkingPool ? 'Checking…' : 'Check pool'}
          </Button>
          <Button disabled={!selectedProvider} onClick={() => { model.clearError(); setImporting(true) }}>
            <ClipboardPaste size={16} aria-hidden="true" />Bulk import
          </Button>
          <Button variant="primary" disabled={!selectedProvider} onClick={() => { model.clearError(); setEditor({ mode: 'add' }) }}>
            <Plus size={16} aria-hidden="true" />Add key
          </Button>
        </div>
      </header>

      {state.poolReport ? (
        <div className={styles.poolReport} data-rejected={state.poolReport.rejected > 0 || undefined} role="status">
          <div>
            <strong>
              {state.poolReport.reachable
                ? state.poolReport.rejected === 0
                  ? `All ${state.poolReport.checked} keys answered`
                  : `${state.poolReport.rejected} of ${state.poolReport.checked} keys were rejected`
                : 'The provider did not answer the check'}
            </strong>
            <span>
              {state.poolReport.reachable
                ? state.poolReport.rejected === 0
                  ? 'Every credential still authenticates.'
                  : 'Rejected keys kept answering 401 on their own catalog request. The rows below carry their verdicts.'
                : 'No verdict about the keys: an unreachable provider says nothing about its credentials.'}
            </span>
          </div>
          {rejectedCount > 0 ? <Button variant="danger" disabled={Boolean(state.pendingId)} onClick={() => void model.removeRejected()}>Remove {rejectedCount} rejected</Button> : null}
        </div>
      ) : null}

      {state.error ? <div className={styles.error} role="alert">{state.error}</div> : null}
      <div className={styles.tableWrap}>
        <table className={styles.table}>
          <thead>
            <tr>
              <th>Priority</th><th>Label</th><th>Limit</th><th>Actual</th><th>Proxy</th><th>State</th><th>429</th><th>Actions</th>
            </tr>
          </thead>
          <tbody>
            {ordered.map((key, index) => {
              const movablePosition = movablePositions.get(key.id) ?? -1
              const keyState = stateOfKey(key)
              return (
                <tr key={key.id} data-dead={key.authStreak >= 3 || undefined}>
                  <td className={styles.priority}>{index + 1}</td>
                  <td>
                    <span className={styles.keyLabel}>
                      <KeyRound size={15} aria-hidden="true" />
                      <span>
                        <strong>{key.label}</strong>
                        <small>
                          {key.authStreak >= 3
                            ? 'Looks dead — revoke and replace it'
                            : key.pinned ? 'Managed · direct IP' : 'Encrypted local key'}
                        </small>
                      </span>
                      {key.lastOutcome ? <span className={styles.outcome} data-outcome={key.lastOutcome}>{key.lastOutcome}</span> : null}
                    </span>
                  </td>
                  <td className={styles.num}>{key.rpm === 0 ? 'Unlimited' : key.rpm}</td>
                  <td className={styles.num}>{key.startsInWindow} / {perSecond ? 's' : 'min'}</td>
                  <td>{key.pinned ? 'Direct' : key.proxyConfigured ? 'Configured' : 'Direct'}</td>
                  <td><Pill tone={keyState.tone}>{keyState.label}</Pill></td>
                  <td className={styles.num}>{key.retries429}</td>
                  <td>
                    <div className={styles.rowActions}>
                      <IconAction label={`Move ${key.label} up`} disabled={key.pinned || movablePosition === 0 || state.pendingId === key.id} onClick={() => void model.move(key.id, -1)}><ArrowUp /></IconAction>
                      <IconAction label={`Move ${key.label} down`} disabled={key.pinned || movablePosition === movablePositions.size - 1 || state.pendingId === key.id} onClick={() => void model.move(key.id, 1)}><ArrowDown /></IconAction>
                      <IconAction label={`Edit ${key.label}`} disabled={Boolean(state.pendingId)} onClick={() => { model.clearError(); setEditor({ mode: 'edit', key }) }}><Pencil /></IconAction>
                      <IconAction label={`Reset cooldown for ${key.label}`} disabled={Boolean(state.pendingId)} onClick={() => void model.reset(key.id)}><RefreshCcw /></IconAction>
                      <IconAction label={`Remove ${key.label}`} danger disabled={key.pinned || Boolean(state.pendingId)} onClick={() => { model.clearError(); setRemoveKey(key) }}><Trash2 /></IconAction>
                    </div>
                  </td>
                </tr>
              )
            })}
            {state.phase !== 'loading' && ordered.length === 0 ? <tr><td colSpan={8} className={styles.empty}>No keys configured for this provider.</td></tr> : null}
          </tbody>
        </table>
      </div>
      <footer className={styles.footer}>
        <span>{ordered.length} keys · lower number means higher priority</span>
        <span>Secrets and proxy credentials are write-only</span>
      </footer>

      {editor ? (
        <KeyEditor
          providerId={selectedProvider}
          unit={unit}
          mode={editor.mode}
          keyValue={editor.key}
          pending={Boolean(state.pendingId)}
          operationError={state.error}
          onClose={() => setEditor(null)}
          onSubmit={async (value) => {
            const saved = editor.mode === 'add'
              ? await model.add(value as AddApiKey)
              : await model.update(value as UpdateApiKey)
            if (saved) setEditor(null)
          }}
        />
      ) : null}
      {importing ? (
        <BulkImport
          providerId={selectedProvider}
          unit={unit}
          pending={Boolean(state.pendingId)}
          operationError={state.error}
          onClose={() => setImporting(false)}
          onImport={(value) => model.importKeys(value)}
        />
      ) : null}
      {removeKey ? (
        <ConfirmRemove
          keyValue={removeKey}
          pending={Boolean(state.pendingId)}
          error={state.error}
          onCancel={() => setRemoveKey(null)}
          onConfirm={async () => {
            if (await model.remove(removeKey.id)) setRemoveKey(null)
          }}
        />
      ) : null}
    </section>
  )
}

function IconAction({ label, disabled, danger = false, onClick, children }: { label: string; disabled?: boolean; danger?: boolean; onClick: () => void; children: ReactElement<{ size?: number }> }) {
  return <button type="button" className={styles.iconAction} data-danger={danger} aria-label={label} title={label} disabled={disabled} onClick={onClick}>{children}</button>
}

function KeyEditor({ providerId, unit, mode, keyValue, pending, operationError, onClose, onSubmit }: {
  providerId: string
  unit: 'minute' | 'second'
  mode: 'add' | 'edit'
  keyValue?: ApiKey
  pending: boolean
  operationError: string
  onClose: () => void
  onSubmit: (value: AddApiKey | UpdateApiKey) => Promise<void>
}) {
  const [label, setLabel] = useState(keyValue?.label ?? '')
  const [rpm, setRPM] = useState(String(keyValue?.rpm ?? 0))
  const [secret, setSecret] = useState('')
  const [replaceProxy, setReplaceProxy] = useState(mode === 'add')
  const [proxyUrl, setProxyURL] = useState('')
  const [error, setError] = useState('')
  const dialogRef = useModalFocus<HTMLFormElement>(onClose, pending)

  const submit = (event: FormEvent) => {
    event.preventDefault()
    const parsedRPM = Number(rpm)
    if (!label.trim() || !Number.isInteger(parsedRPM) || parsedRPM < 0 || parsedRPM > 1_000_000 || (mode === 'add' && !secret.trim())) {
      setError('Enter a label, a limit from 0 to 1,000,000, and the key secret.')
      return
    }
    if (replaceProxy && !proxyURLIsValid(proxyUrl)) {
      setError('Enter a valid HTTP(S) or SOCKS5 proxy URL without a fragment.')
      return
    }
    setError('')
    if (mode === 'add') {
      void onSubmit({ providerId, label: label.trim(), secret, rpm: parsedRPM, proxyUrl: proxyUrl.trim() })
      return
    }
    const value: UpdateApiKey = {
      providerId,
      keyId: keyValue!.id,
      label: label.trim(),
      rpm: parsedRPM,
      ...(replaceProxy && !keyValue?.pinned ? { proxyUrl: proxyUrl.trim() } : {}),
      ...(secret.trim() && !keyValue?.pinned ? { secret } : {}),
    }
    void onSubmit(value)
  }

  return (
    <div className="ui-scrim" role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget && !pending) onClose() }}>
      <form ref={dialogRef} className="ui-modal" role="dialog" aria-modal="true" aria-label={mode === 'add' ? 'Add API key' : 'Edit API key'} onSubmit={submit}>
        <header><div><h2>{mode === 'add' ? 'Add API key' : 'Edit API key'}</h2><p>The secret is encrypted locally and never shown again.</p></div><button type="button" aria-label="Close" disabled={pending} onClick={onClose}><X size={18} /></button></header>
        <div className={styles.formBody}>
          <label><span>Label</span><input value={label} maxLength={80} data-autofocus onChange={(event) => setLabel(event.currentTarget.value)} /></label>
          <label><span>Requests per {unit}</span><input type="number" min="0" max="1000000" step="1" required value={rpm} onChange={(event) => setRPM(event.currentTarget.value)} /><small>0 means unlimited. The window follows the provider's rate unit.</small></label>
          {!keyValue?.pinned ? <label><span>{mode === 'add' ? 'API key' : 'Replace API key (optional)'}</span><input type="password" value={secret} maxLength={8192} autoComplete="new-password" onChange={(event) => setSecret(event.currentTarget.value)} /></label> : <div className={styles.pinnedNote}>Managed key material is write-only; the request limit can still be changed.</div>}
          {!keyValue?.pinned ? <div className={styles.proxyField}><span>Proxy</span>{mode === 'edit' ? <label className={styles.check}><input type="checkbox" checked={replaceProxy} onChange={(event) => setReplaceProxy(event.currentTarget.checked)} />Replace current proxy setting</label> : null}<input aria-label="Proxy URL" type="password" disabled={!replaceProxy} value={proxyUrl} maxLength={8192} placeholder="Optional http(s) or socks5 URL" autoComplete="new-password" onChange={(event) => setProxyURL(event.currentTarget.value)} /><small>Leave empty to use the native IP.</small></div> : null}
          {error || operationError ? <p className={styles.formError} role="alert">{error || operationError}</p> : null}
        </div>
        <footer><Button type="button" disabled={pending} onClick={onClose}>Cancel</Button><Button type="submit" variant="primary" disabled={pending}>{pending ? 'Saving…' : 'Save key'}</Button></footer>
      </form>
    </div>
  )
}

function BulkImport({ providerId, unit, pending, operationError, onClose, onImport }: {
  providerId: string
  unit: 'minute' | 'second'
  pending: boolean
  operationError: string
  onClose: () => void
  onImport: (value: ImportApiKeys) => Promise<ImportApiKeysReport | null>
}) {
  const [text, setText] = useState('')
  const [rpm, setRPM] = useState('0')
  const [proxyUrl, setProxyURL] = useState('')
  const [error, setError] = useState('')
  const [result, setResult] = useState<{ report: ImportApiKeysReport; entries: readonly PastedKey[] } | null>(null)
  const dialogRef = useModalFocus<HTMLFormElement>(onClose, pending)
  const parsed = useMemo(() => parsePastedKeys(text), [text])
  // A short screen scrolls the dialog, and its footer sticks over the last rows, so
  // the outcome is scrolled to instead of being left behind the buttons.
  useEffect(() => {
    if (result) dialogRef.current?.scrollTo({ top: dialogRef.current.scrollHeight })
  }, [dialogRef, result])

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    const parsedRPM = Number(rpm)
    if (parsed.length === 0) {
      setError('Paste at least one key, its label and secret on the same line.')
      return
    }
    if (parsed.length > MAX_IMPORT_KEYS) {
      setError(`Import at most ${MAX_IMPORT_KEYS} keys at once; split the list.`)
      return
    }
    // The command travels as one protocol frame the desktop shell caps at
    // 256 KiB. Pasting the wrong clipboard (a config, a log) used to exceed it
    // and die as a generic transport failure; measuring the exact serialized
    // size says it in a sentence the caller can act on.
    if (JSON.stringify(parsed).length > MAX_IMPORT_FRAME_CHARS) {
      setError('This paste is too large for one command. Split it into smaller batches, or check the wrong clipboard did not land here.')
      return
    }
    if (!Number.isInteger(parsedRPM) || parsedRPM < 0 || parsedRPM > 1_000_000) {
      setError('Enter a limit from 0 to 1,000,000.')
      return
    }
    if (!proxyURLIsValid(proxyUrl)) {
      setError('Enter a valid HTTP(S) or SOCKS5 proxy URL without a fragment.')
      return
    }
    setError('')
    const report = await onImport({ providerId, rpm: parsedRPM, proxyUrl: proxyUrl.trim(), entries: parsed })
    if (report) setResult({ report, entries: parsed })
  }

  return (
    <div className="ui-scrim" role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget && !pending) onClose() }}>
      <form ref={dialogRef} className={`ui-modal ${styles.import}`} role="dialog" aria-modal="true" aria-label="Bulk import API keys" onSubmit={(event) => void submit(event)}>
        <header><div><h2>Bulk import API keys</h2><p>One key per line, its label first and its secret last. Keys already configured are skipped.</p></div><button type="button" aria-label="Close" disabled={pending} onClick={onClose}><X size={18} /></button></header>
        <div className={styles.formBody}>
          <label>
            <span>Keys</span>
            <textarea
              className={styles.paste}
              value={text}
              rows={9}
              spellCheck={false}
              maxLength={262_144}
              data-autofocus
              placeholder={'team_alpha sk-first-secret\nteam_beta sk-second-secret'}
              onChange={(event) => setText(event.currentTarget.value)}
            />
            <small>{parsed.length === 0 ? 'Label and secret separated by a space, a tab or a comma.' : `${parsed.length} ${parsed.length === 1 ? 'key' : 'keys'} ready · the limit and proxy below apply to all of them.`}</small>
          </label>
          <label><span>Requests per {unit}</span><input type="number" min="0" max="1000000" step="1" required value={rpm} onChange={(event) => setRPM(event.currentTarget.value)} /><small>0 means unlimited. Applies to every imported key.</small></label>
          <div className={styles.proxyField}>
            <span>Proxy</span>
            <input aria-label="Proxy URL" type="password" value={proxyUrl} maxLength={8192} placeholder="Optional http(s) or socks5 URL" autoComplete="new-password" onChange={(event) => setProxyURL(event.currentTarget.value)} />
            <small>Applies to every imported key. Leave empty to use the native IP.</small>
          </div>
          {result ? <ImportSummary report={result.report} entries={result.entries} /> : null}
          {error || operationError ? <p className={styles.formError} role="alert">{error || operationError}</p> : null}
        </div>
        <footer><Button type="button" disabled={pending} onClick={onClose}>{result ? 'Done' : 'Cancel'}</Button><Button type="submit" variant="primary" disabled={pending || parsed.length === 0}>{pending ? 'Importing…' : parsed.length > 0 ? `Import ${parsed.length} ${parsed.length === 1 ? 'key' : 'keys'}` : 'Import keys'}</Button></footer>
      </form>
    </div>
  )
}

function ImportSummary({ report, entries }: { report: ImportApiKeysReport; entries: readonly PastedKey[] }) {
  return (
    <div className={styles.importSummary} data-added={report.added > 0} role="status">
      <strong>{report.added === 0 ? 'Nothing new to add' : `Added ${report.added} of ${entries.length} ${entries.length === 1 ? 'key' : 'keys'}`}</strong>
      {report.duplicate.length > 0 ? <span>{report.duplicate.length} already configured: {names(report.duplicate, entries)}</span> : null}
      {report.rejected.length > 0 ? <span>{report.rejected.length} could not be read: {lines(report.rejected)}</span> : null}
    </div>
  )
}

// Labels are what the caller typed, so they can be shown back; a long list is cut
// to a readable head with the rest counted rather than silently dropped.
function names(positions: readonly number[], entries: readonly PastedKey[]): string {
  const labels = positions.map((position) => entries[position]?.label ?? `line ${position + 1}`)
  return labels.length > 6 ? `${labels.slice(0, 6).join(', ')} and ${labels.length - 6} more` : labels.join(', ')
}

function lines(positions: readonly number[]): string {
  const shown = positions.slice(0, 6).map((position) => position + 1).join(', ')
  if (positions.length > 6) return `entries ${shown} and ${positions.length - 6} more`
  return positions.length === 1 ? `entry ${shown}` : `entries ${shown}`
}

function ConfirmRemove({ keyValue, pending, error, onCancel, onConfirm }: { keyValue: ApiKey; pending: boolean; error: string; onCancel: () => void; onConfirm: () => Promise<void> }) {
  const dialogRef = useModalFocus<HTMLElement>(onCancel, pending)
  return <div className="ui-scrim"><section ref={dialogRef} className={`ui-modal ${styles.confirm}`} role="dialog" aria-modal="true" aria-label="Remove API key"><header><div><h2>Remove “{keyValue.label}”?</h2><p>Queued requests will use the next eligible key.</p></div></header>{error ? <p className={styles.confirmError} role="alert">{error}</p> : null}<footer><Button disabled={pending} onClick={onCancel}>Cancel</Button><Button variant="danger" disabled={pending} onClick={() => void onConfirm()}>{pending ? 'Removing…' : 'Remove key'}</Button></footer></section></div>
}

function stateOfKey(key: ApiKey): { label: string; tone: 'neutral' | 'success' | 'warning' | 'danger' } {
  if (key.authStreak >= 3) return { label: 'Dead', tone: 'danger' }
  if (key.cooldownMs > 0) return { label: key.cooldownMs < 60_000 ? `Cooldown ${Math.ceil(key.cooldownMs / 1_000)} s` : `Cooldown ${Math.ceil(key.cooldownMs / 60_000)} min`, tone: 'warning' }
  if (key.blockedModels > 0) return { label: `${key.blockedModels} model${key.blockedModels === 1 ? '' : 's'} blocked`, tone: 'warning' }
  return { label: 'Ready', tone: 'success' }
}
