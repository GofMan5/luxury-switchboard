import { useState, type FormEvent } from 'react'
import { KeyRound, Pencil, Plus, RefreshCw, ShieldCheck, Trash2, X } from 'lucide-react'
import { Button } from '../../../shared/ui/Button'
import { StatusDot } from '../../../shared/ui/StatusDot'
import type { Provider, ProviderInput } from '../domain/provider'
import { useProviders } from './useProviders'
import styles from './ProvidersPage.module.css'

export default function ProvidersPage() {
  const { model, state } = useProviders()
  const [selectedID, setSelectedID] = useState('')
  const [editor, setEditor] = useState<{ mode: 'add' | 'edit'; provider?: Provider } | null>(null)
  const [removeProvider, setRemoveProvider] = useState<Provider | null>(null)
  const effectiveID = selectedID || state.catalog.activeId
  const selected = state.catalog.providers.find((provider) => provider.id === effectiveID)

  return (
    <section className={styles.page}>
      <header className="page-header">
        <div><h1>Providers</h1><p>Endpoints, limits and active routing</p></div>
        <div className={styles.headerActions}>
          <Button variant="secondary" disabled={state.phase === 'loading'} onClick={() => void model.refresh()}><RefreshCw size={15} />Refresh</Button>
          <Button variant="primary" onClick={() => setEditor({ mode: 'add' })}><Plus size={16} />Add provider</Button>
        </div>
      </header>

      {state.error ? <div className={styles.error} role="alert">{state.error}</div> : null}
      <div className={styles.layout}>
        <section className={styles.listPane} aria-label="Configured providers">
          <div className={styles.listHeader}><span>Name</span><span>Health</span><span>RPM</span><span>Route</span></div>
          {state.catalog.providers.map((provider) => {
            const active = provider.id === state.catalog.activeId
            return (
              <button key={provider.id} type="button" className={styles.providerRow} data-selected={provider.id === effectiveID} onClick={() => setSelectedID(provider.id)}>
                <span className={styles.providerName}><strong>{provider.name}</strong><small>{provider.builtin ? 'Built-in provider' : provider.keyCount > 0 ? `${provider.keyCount} configured keys` : 'Custom provider'}</small></span>
                <span className={styles.health}><StatusDot state={provider.enabled ? 'healthy' : 'stopped'} />{provider.enabled ? 'Ready' : 'Disabled'}</span>
                <span className={styles.rpm}>{provider.rpm === 0 ? '∞' : provider.rpm}</span>
                <span className={active ? styles.active : styles.standby}>{active ? 'Active' : 'Standby'}</span>
              </button>
            )
          })}
          {state.catalog.providers.length === 0 ? <div className={styles.empty}>No providers configured.</div> : null}
        </section>

        {selected ? (
          <ProviderInspector
            provider={selected}
            active={selected.id === state.catalog.activeId}
            pending={state.pendingId === selected.id}
            onActivate={() => void model.activate(selected.id)}
            onEdit={() => setEditor({ mode: 'edit', provider: selected })}
            onDelete={() => setRemoveProvider(selected)}
          />
        ) : <div className={styles.noSelection}>Select a provider to inspect its policy.</div>}
      </div>

      {editor ? (
        <ProviderEditor
          mode={editor.mode}
          provider={editor.provider}
          pending={Boolean(state.pendingId)}
          active={editor.provider?.id === state.catalog.activeId}
          onClose={() => setEditor(null)}
          onSubmit={async (value) => {
            const saved = editor.mode === 'add'
              ? await model.add(value)
              : await model.update(editor.provider!.id, value)
            if (saved) setEditor(null)
          }}
        />
      ) : null}
      {removeProvider ? (
        <ConfirmDelete
          provider={removeProvider}
          pending={Boolean(state.pendingId)}
          onCancel={() => setRemoveProvider(null)}
          onConfirm={async () => {
            if (await model.delete(removeProvider.id)) {
              setSelectedID('')
              setRemoveProvider(null)
            }
          }}
        />
      ) : null}
    </section>
  )
}

function ProviderInspector({ provider, active, pending, onActivate, onEdit, onDelete }: {
  provider: Provider; active: boolean; pending: boolean
  onActivate: () => void; onEdit: () => void; onDelete: () => void
}) {
  return (
    <aside className={styles.inspector} aria-label={`${provider.name} provider details`}>
      <header className={styles.inspectorHeader}>
        <div><h2>{provider.name}</h2><span><StatusDot state={provider.enabled ? 'healthy' : 'stopped'} />{provider.enabled ? 'Configured' : 'Disabled'}</span></div>
        <div className={styles.inspectorActions}>
          <Button variant="secondary" onClick={onEdit}><Pencil size={14} />Edit</Button>
          {!provider.builtin ? <Button variant="danger" disabled={active || provider.keyCount > 0} onClick={onDelete}><Trash2 size={14} />Delete</Button> : null}
          <Button variant={active ? 'secondary' : 'primary'} disabled={active || pending || !provider.enabled} onClick={onActivate}>{pending ? 'Switching…' : active ? 'Active route' : 'Activate'}</Button>
        </div>
      </header>
      <section className={styles.section}><h3>Identity</h3><dl><dt>Display name</dt><dd>{provider.name}</dd><dt>Base URL</dt><dd className={styles.endpoint}>{provider.baseUrl}</dd><dt>Dialect</dt><dd>{provider.dialect}</dd><dt>Models path</dt><dd className={styles.endpoint}>{provider.modelsPath}</dd><dt>Authentication</dt><dd>{provider.authMode === 'custom' ? provider.authHeader : provider.authMode}</dd></dl></section>
      <section className={styles.section}><h3>Traffic policy</h3><dl><dt>Requests per minute</dt><dd>{provider.rpm === 0 ? 'Unlimited' : provider.rpm}</dd><dt>Prompt cache TTL</dt><dd>{provider.cacheTtl === '1h0m0s' ? '1 hour' : 'Provider default'}</dd><dt>Relay behavior</dt><dd>{active ? 'Receives unassigned models' : 'Available for model routes'}</dd></dl></section>
      <section className={styles.section}><h3>Authentication</h3><div className={styles.authRow}>{provider.authMode === 'passthrough' ? <ShieldCheck size={18} /> : <KeyRound size={18} />}<div><strong>{provider.authMode === 'passthrough' ? 'Forwarded from local client' : provider.keyCount > 0 ? `${provider.keyCount} encrypted keys` : 'No API keys configured'}</strong><span>Secret material is never returned to the UI.</span></div></div></section>
    </aside>
  )
}

function ProviderEditor({ mode, provider, pending, active, onClose, onSubmit }: {
  mode: 'add' | 'edit'; provider?: Provider; pending: boolean; active: boolean
  onClose: () => void; onSubmit: (value: ProviderInput) => Promise<void>
}) {
  const [name, setName] = useState(provider?.name ?? '')
  const [baseUrl, setBaseURL] = useState(provider?.baseUrl ?? '')
  const [authMode, setAuthMode] = useState<Provider['authMode']>(provider?.authMode ?? 'bearer')
  const [authHeader, setAuthHeader] = useState(provider?.authHeader ?? '')
  const [dialect, setDialect] = useState<Provider['dialect']>(provider?.dialect ?? 'auto')
  const [modelsPath, setModelsPath] = useState(provider?.modelsPath ?? '/v1/models')
  const [rpm, setRPM] = useState(String(provider?.rpm ?? 0))
  const [cache1h, setCache1H] = useState(provider?.cacheTtl === '1h0m0s')
  const [enabled, setEnabled] = useState(provider?.enabled ?? true)
  const [error, setError] = useState('')

  const submit = (event: FormEvent) => {
    event.preventDefault()
    const parsedRPM = Number(rpm)
    if (!name.trim() || !baseUrl.trim() || !modelsPath.startsWith('/') || (authMode === 'custom' && !authHeader.trim()) || !Number.isInteger(parsedRPM) || parsedRPM < 0 || (active && !enabled)) {
      setError(active && !enabled ? 'Switch away from the provider before disabling it.' : 'Enter a name, an absolute endpoint and a non-negative RPM.')
      return
    }
    setError('')
    void onSubmit({ name: name.trim(), baseUrl: baseUrl.trim(), authMode, authHeader: authHeader.trim(), dialect, modelsPath: modelsPath.trim(), rpm: parsedRPM, cache1h, enabled })
  }

  return (
    <div className="ui-scrim" onMouseDown={(event) => { if (event.target === event.currentTarget && !pending) onClose() }}>
      <form className={`ui-modal ${styles.providerModal}`} role="dialog" aria-modal="true" aria-label={mode === 'add' ? 'Add provider' : 'Edit provider'} onSubmit={submit}>
        <header><div><h2>{mode === 'add' ? 'Add provider' : `Edit ${provider?.name}`}</h2><p>Remote endpoints require HTTPS; loopback HTTP is allowed.</p></div><button type="button" aria-label="Close" disabled={pending} onClick={onClose}><X size={18} /></button></header>
        <div className={styles.formBody}>
          <label><span>Display name</span><input value={name} maxLength={80} autoFocus onChange={(event) => setName(event.currentTarget.value)} /></label>
          <label><span>Base URL</span><input type="url" value={baseUrl} placeholder="https://provider.example/v1" onChange={(event) => setBaseURL(event.currentTarget.value)} /></label>
          <label><span>API dialect</span><select value={dialect} onChange={(event) => setDialect(event.currentTarget.value as Provider['dialect'])}><option value="auto">Auto-detect from request</option><option value="openai">OpenAI compatible</option><option value="anthropic">Anthropic compatible</option></select></label>
          <label><span>Models discovery path</span><input value={modelsPath} placeholder="/v1/models" onChange={(event) => setModelsPath(event.currentTarget.value)} /></label>
          <label><span>Authentication</span><select value={authMode} onChange={(event) => setAuthMode(event.currentTarget.value as Provider['authMode'])}><option value="auto">Auto by API dialect</option><option value="bearer">Bearer token</option><option value="x-api-key">x-api-key</option><option value="custom">Custom header</option><option value="passthrough">Pass through client auth</option></select></label>
          {authMode === 'custom' ? <label><span>Custom auth header</span><input value={authHeader} placeholder="api-key" onChange={(event) => setAuthHeader(event.currentTarget.value)} /><small>Header name only. The value comes from the encrypted key pool.</small></label> : null}
          <label><span>Provider RPM</span><input type="number" min="0" step="1" value={rpm} onChange={(event) => setRPM(event.currentTarget.value)} /><small>0 means unlimited. Per-key RPM still applies.</small></label>
          <label className={styles.check}><input type="checkbox" checked={cache1h} onChange={(event) => setCache1H(event.currentTarget.checked)} />Extend existing ephemeral cache controls to 1 hour</label>
          <label className={styles.check}><input type="checkbox" checked={enabled} disabled={active} onChange={(event) => setEnabled(event.currentTarget.checked)} />Provider enabled</label>
          {error ? <p className={styles.formError} role="alert">{error}</p> : null}
        </div>
        <footer><Button type="button" disabled={pending} onClick={onClose}>Cancel</Button><Button type="submit" variant="primary" disabled={pending}>{pending ? 'Saving…' : 'Save provider'}</Button></footer>
      </form>
    </div>
  )
}

function ConfirmDelete({ provider, pending, onCancel, onConfirm }: { provider: Provider; pending: boolean; onCancel: () => void; onConfirm: () => Promise<void> }) {
  return <div className="ui-scrim"><section className={`ui-modal ${styles.confirm}`} role="dialog" aria-modal="true" aria-label="Delete provider"><header><div><h2>Delete “{provider.name}”?</h2><p>This removes only provider settings. Requests are not affected because active providers cannot be deleted.</p></div></header><footer><Button disabled={pending} onClick={onCancel}>Cancel</Button><Button variant="danger" disabled={pending} onClick={() => void onConfirm()}>{pending ? 'Deleting…' : 'Delete provider'}</Button></footer></section></div>
}
