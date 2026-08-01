import { useEffect, useMemo, useState, type FormEvent, type ReactElement } from 'react'
import { ArrowDown, ArrowUp, KeyRound, Pencil, Plus, RefreshCcw, Trash2, X } from 'lucide-react'
import { useProviders } from '../../providers/ui/useProviders'
import { Button } from '../../../shared/ui/Button'
import { useModalFocus } from '../../../shared/ui/useModalFocus'
import type { AddApiKey, ApiKey, UpdateApiKey } from '../domain/api-key'
import { useApiKeys } from './useApiKeys'
import styles from './ApiKeysPage.module.css'

export default function ApiKeysPage() {
  const { state: providers } = useProviders()
  const { model, state } = useApiKeys()
  const [providerID, setProviderID] = useState('')
  const [editor, setEditor] = useState<{ mode: 'add' | 'edit'; key?: ApiKey } | null>(null)
  const [removeKey, setRemoveKey] = useState<ApiKey | null>(null)
  const selectedProvider = providerID || providers.catalog.activeId || providers.catalog.providers[0]?.id || ''

  useEffect(() => {
    if (selectedProvider) void model.load(selectedProvider)
  }, [model, selectedProvider])

  const ordered = useMemo(
    () => [...state.keys].sort((left, right) => left.priority - right.priority),
    [state.keys],
  )
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
          <Button variant="primary" disabled={!selectedProvider} onClick={() => setEditor({ mode: 'add' })}>
            <Plus size={16} aria-hidden="true" />Add key
          </Button>
        </div>
      </header>

      {state.error ? <div className={styles.error} role="alert">{state.error}</div> : null}
      <div className={styles.tableWrap}>
        <table className={styles.table}>
          <thead>
            <tr>
              <th>Priority</th><th>Label</th><th>RPM</th><th>Actual</th><th>Proxy</th><th>Cooldown</th><th>429</th><th>Actions</th>
            </tr>
          </thead>
          <tbody>
            {ordered.map((key, index) => {
              const movablePosition = movablePositions.get(key.id) ?? -1
              return (
                <tr key={key.id}>
                  <td className={styles.priority}>{index + 1}</td>
                  <td>
                    <span className={styles.keyLabel}><KeyRound size={15} aria-hidden="true" /><span><strong>{key.label}</strong><small>{key.pinned ? 'Environment · direct IP' : 'Encrypted local key'}</small></span></span>
                  </td>
                  <td>{key.rpm === 0 ? 'Unlimited' : key.rpm}</td>
                  <td>{key.startsInWindow} / min</td>
                  <td>{key.pinned ? 'Direct' : key.proxyConfigured ? 'Configured' : 'Direct'}</td>
                  <td>{formatCooldown(key.cooldownMs, key.blockedModels)}</td>
                  <td>{key.retries429}</td>
                  <td>
                    <div className={styles.rowActions}>
                      <IconAction label={`Move ${key.label} up`} disabled={key.pinned || movablePosition === 0 || state.pendingId === key.id} onClick={() => void model.move(key.id, -1)}><ArrowUp /></IconAction>
                      <IconAction label={`Move ${key.label} down`} disabled={key.pinned || movablePosition === movablePositions.size - 1 || state.pendingId === key.id} onClick={() => void model.move(key.id, 1)}><ArrowDown /></IconAction>
                      <IconAction label={`Edit ${key.label}`} disabled={Boolean(state.pendingId)} onClick={() => setEditor({ mode: 'edit', key })}><Pencil /></IconAction>
                      <IconAction label={`Reset cooldown for ${key.label}`} disabled={Boolean(state.pendingId)} onClick={() => void model.reset(key.id)}><RefreshCcw /></IconAction>
                      <IconAction label={`Remove ${key.label}`} danger disabled={key.pinned || Boolean(state.pendingId)} onClick={() => setRemoveKey(key)}><Trash2 /></IconAction>
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
          mode={editor.mode}
          keyValue={editor.key}
          pending={Boolean(state.pendingId)}
          onClose={() => setEditor(null)}
          onSubmit={async (value) => {
            const saved = editor.mode === 'add'
              ? await model.add(value as AddApiKey)
              : await model.update(value as UpdateApiKey)
            if (saved) setEditor(null)
          }}
        />
      ) : null}
      {removeKey ? (
        <ConfirmRemove
          keyValue={removeKey}
          pending={Boolean(state.pendingId)}
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

function KeyEditor({ providerId, mode, keyValue, pending, onClose, onSubmit }: {
  providerId: string
  mode: 'add' | 'edit'
  keyValue?: ApiKey
  pending: boolean
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
      setError('Enter a label, a non-negative RPM, and the key secret.')
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
          <label><span>Label</span><input value={label} maxLength={80} autoFocus onChange={(event) => setLabel(event.currentTarget.value)} /></label>
          <label><span>Requests per minute</span><input type="number" min="0" max="1000000" step="1" required value={rpm} onChange={(event) => setRPM(event.currentTarget.value)} /><small>0 means unlimited.</small></label>
          {!keyValue?.pinned ? <label><span>{mode === 'add' ? 'API key' : 'Replace API key (optional)'}</span><input type="password" value={secret} maxLength={8192} autoComplete="new-password" onChange={(event) => setSecret(event.currentTarget.value)} /></label> : <div className={styles.pinnedNote}>Environment key material stays managed by the local account; RPM can still be changed.</div>}
          {!keyValue?.pinned ? <div className={styles.proxyField}><span>Proxy</span>{mode === 'edit' ? <label className={styles.check}><input type="checkbox" checked={replaceProxy} onChange={(event) => setReplaceProxy(event.currentTarget.checked)} />Replace current proxy setting</label> : null}<input aria-label="Proxy URL" type="password" disabled={!replaceProxy} value={proxyUrl} maxLength={8192} placeholder="Optional http(s) or socks5 URL" autoComplete="new-password" onChange={(event) => setProxyURL(event.currentTarget.value)} /><small>Leave empty to use the native IP.</small></div> : null}
          {error ? <p className={styles.formError} role="alert">{error}</p> : null}
        </div>
        <footer><Button type="button" disabled={pending} onClick={onClose}>Cancel</Button><Button type="submit" variant="primary" disabled={pending}>{pending ? 'Saving…' : 'Save key'}</Button></footer>
      </form>
    </div>
  )
}

function ConfirmRemove({ keyValue, pending, onCancel, onConfirm }: { keyValue: ApiKey; pending: boolean; onCancel: () => void; onConfirm: () => Promise<void> }) {
  const dialogRef = useModalFocus<HTMLElement>(onCancel, pending)
  return <div className="ui-scrim"><section ref={dialogRef} className={`ui-modal ${styles.confirm}`} role="dialog" aria-modal="true" aria-label="Remove API key"><header><div><h2>Remove “{keyValue.label}”?</h2><p>Queued requests will use the next eligible key.</p></div></header><footer><Button disabled={pending} onClick={onCancel}>Cancel</Button><Button variant="danger" disabled={pending} onClick={() => void onConfirm()}>{pending ? 'Removing…' : 'Remove key'}</Button></footer></section></div>
}

function formatCooldown(milliseconds: number, blockedModels: number): string {
  if (milliseconds > 0) return milliseconds < 60_000 ? `${Math.ceil(milliseconds / 1_000)} s` : `${Math.ceil(milliseconds / 60_000)} min`
  if (blockedModels > 0) return `${blockedModels} model${blockedModels === 1 ? '' : 's'}`
  return 'Ready'
}
