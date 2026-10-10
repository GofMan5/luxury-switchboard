import { useEffect, useRef, useState, type FormEvent } from 'react'
import { Database, KeyRound, LogIn, Pencil, Plus, RefreshCw, ShieldCheck, Trash2, X } from 'lucide-react'
import { useAppServices } from '../../../app/services'
import { Button } from '../../../shared/ui/Button'
import { EmptyState, Pill } from '../../../shared/ui/chrome'
import { StatusDot } from '../../../shared/ui/StatusDot'
import { useMediaQuery } from '../../../shared/ui/useMediaQuery'
import { useModalFocus } from '../../../shared/ui/useModalFocus'
import { hasCodexLogin } from '../../codex/application/codex-capability'
import type { CodexAccount } from '../../codex/domain/codex'
import DisconnectCodexDialog from '../../codex/ui/DisconnectCodexDialog'
import { useCodex } from '../../codex/ui/useCodex'
import type { Provider, ProviderInput, RateUnit } from '../domain/provider'
import { isCodex } from '../domain/provider'
import { useProviders } from './useProviders'
import AddProviderDialog from './AddProviderDialog'
import { providerInputError } from './provider-form'
import styles from './ProvidersPage.module.css'

export default function ProvidersPage() {
  const { model, state } = useProviders()
  const [selectedID, setSelectedID] = useState('')
  const [editor, setEditor] = useState<{ mode: 'add' | 'edit'; provider?: Provider } | null>(null)
  const [removeProvider, setRemoveProvider] = useState<Provider | null>(null)
  // Below the two-pane comfort width the inspector becomes a sheet over the
  // list, so it opens only on an explicit pick; on wide layouts the active
  // provider is the default read.
  const narrow = useMediaQuery('(max-width: 1120px)')
  const [opened, setOpened] = useState(false)
  // The active-provider fallback is the default read for a fresh page, not a
  // substitute for a requested id: right after Done the catalog can still be
  // stale, so a missing id holds the neutral state until the refresh lands
  // instead of silently showing a different provider.
  const selected = selectedID !== ''
    ? state.catalog.providers.find((provider) => provider.id === selectedID)
    : state.catalog.providers.find((provider) => provider.id === state.catalog.activeId)
  const effectiveID = selected?.id ?? ''
  const inspectorVisible = Boolean(selected) && (!narrow || opened)
  const openProvider = (id: string) => { setSelectedID(id); setOpened(true) }

  const { model: codexModel, state: codexState } = useCodex()
  const { capabilities } = useAppServices()
  // With the Codex preset available, Add provider starts from a chooser; an
  // older control plane never learns about it and keeps the direct route.
  const [addProvider, setAddProvider] = useState<'choose' | 'codex' | null>(null)
  // A disconnect targets one account, so the dialog holds the account it is
  // about to sign out plus how many signed-in accounts survive it.
  const [disconnectCodex, setDisconnectCodex] = useState<{ accountId: string; email: string; otherSignedIn: number } | null>(null)
  const [disconnecting, setDisconnecting] = useState(false)
  // The codex preset delete rides the same logout command with remove; its
  // pending flag is tracked separately from a provider save/switch.
  const [removingCodex, setRemovingCodex] = useState(false)
  const codexRow = state.catalog.providers.find(isCodex) ?? null
  // Codex provisioning writes through the provider registry without a
  // providers.changed push, so a sign-in that exits any way other than Done
  // would leave the list without the preset row until a manual Refresh. One
  // catalog refresh per success covers every exit; the row check in
  // selectCodexRow then finds the row present and Done does not refresh twice.
  const codexSigninRefreshed = useRef(false)
  useEffect(() => {
    if (codexState.loginPhase !== 'success') {
      codexSigninRefreshed.current = false
      return
    }
    if (codexSigninRefreshed.current) return
    codexSigninRefreshed.current = true
    void model.refresh()
  }, [codexState.loginPhase, model])

  const openAddFlow = () => {
    if (hasCodexLogin(capabilities)) {
      setAddProvider('choose')
      return
    }
    model.clearError()
    setEditor({ mode: 'add' })
  }
  const openCustomEditor = () => {
    setAddProvider(null)
    model.clearError()
    setEditor({ mode: 'add' })
  }
  const selectCodexRow = () => {
    // Every account of the preset resolves to the same provider entry, so
    // any account that carries a provider id selects it; before the first
    // sign-in the list row is the only id there is.
    const id = codexState.accounts.find((account) => account.providerId !== '')?.providerId ?? codexRow?.id
    setAddProvider(null)
    if (!id) return
    // The first sign-in creates the preset row, so the catalog may need a
    // refresh before the inspector can resolve the selection. A success has
    // already refreshed it, so Done does not ask twice.
    if (!codexSigninRefreshed.current && !state.catalog.providers.some((provider) => provider.id === id)) void model.refresh()
    openProvider(id)
  }
  const signInCodexAgain = () => {
    setAddProvider('codex')
    void codexModel.startLogin()
  }
  // A disconnect targets one account: the dialog names it and says how many
  // signed-in accounts survive, so the owner knows what stays working.
  const beginCodexDisconnect = (accountId: string) => {
    const account = codexState.accounts.find((entry) => entry.accountId === accountId)
    if (!account) return
    const otherSignedIn = codexState.accounts.filter((entry) => entry.accountId !== accountId && entry.state === 'signed_in').length
    setDisconnectCodex({ accountId, email: account.email, otherSignedIn })
  }
  // The list row stands for every account: it leads with the first
  // signed-in email and folds the rest into a count.
  const codexSignedInEmails = codexState.accounts.filter((account) => account.state === 'signed_in').map((account) => account.email)
  const codexCaption = codexSignedInEmails.length === 0 ? 'Codex preset'
    : codexSignedInEmails.length === 1 ? `Codex preset · ${codexSignedInEmails[0]}`
    : `Codex preset · ${codexSignedInEmails[0]} +${codexSignedInEmails.length - 1}`

  return (
    <section className={styles.page}>
      <header className="page-header">
        <div><h1>Providers</h1><p>Endpoints, limits and active routing</p></div>
        <div className={styles.headerActions}>
          <Button variant="secondary" disabled={state.phase === 'loading'} onClick={() => void model.refresh()}><RefreshCw size={15} />Refresh</Button>
          <Button variant="primary" onClick={openAddFlow}><Plus size={16} />Add provider</Button>
        </div>
      </header>

      {state.error ? <div className={styles.error} role="alert">{state.error}</div> : null}
      <div className={styles.layout}>
        <section className={styles.listPane} aria-label="Configured providers">
          <div className={styles.listHeader}><span>Name</span><span>State</span><span>Rate</span><span>Route</span></div>
          <div className={styles.listScroll}>
            {state.catalog.providers.map((provider) => {
              const active = provider.id === state.catalog.activeId
              const health = state.health.get(provider.id)
              // Codex rows carry the accounts, not key counts. Their state
              // column reports the sign-in first: a preset without a
              // signed-in account routes nothing, so that is the state that
              // matters. The aggregate mirrors the model's: any live
              // account keeps the preset live.
              const codexNeedsSignIn = isCodex(provider) && provider.enabled && codexState.state !== 'signed_in'
              return (
                <button key={provider.id} type="button" className={styles.providerRow} data-selected={provider.id === effectiveID} onClick={() => openProvider(provider.id)}>
                  <span className={styles.providerName}><strong>{provider.name}</strong><small>{isCodex(provider) ? codexCaption : provider.builtin ? 'Built-in provider' : provider.keyCount > 0 ? `${provider.keyCount} configured keys` : 'Custom provider'}</small></span>
                  {codexNeedsSignIn ? (
                    <span className={styles.health} title={codexState.state === 'reauth_needed' ? 'The Codex session expired. Sign in again from the provider details.' : undefined}>
                      <StatusDot state={codexState.state === 'reauth_needed' ? 'degraded' : 'stopped'} />
                      {codexState.state === 'reauth_needed' ? 'Sign-in needed' : 'Signed out'}
                    </span>
                  ) : (
                    <span className={styles.health} title={health && !health.up ? health.reason : undefined}>
                      <StatusDot state={health ? (health.up ? 'healthy' : 'failed') : provider.enabled ? 'healthy' : 'stopped'} />
                      {provider.enabled ? (health && !health.up ? 'Unreachable' : 'Enabled') : 'Disabled'}
                    </span>
                  )}
                  <span className={styles.rpm}>{rateShort(provider)}</span>
                  {active ? <Pill tone="info">Active</Pill> : <Pill>Standby</Pill>}
                </button>
              )
            })}
            {state.catalog.providers.length === 0 ? <EmptyState icon={Database} title="No providers configured" hint="Add a provider to route requests through the relay." /> : null}
          </div>
        </section>

        {inspectorVisible && selected ? (
          <ProviderInspector
            provider={selected}
            active={selected.id === state.catalog.activeId}
            pending={state.pendingId === selected.id}
            codexAccounts={isCodex(selected) ? codexState.accounts : []}
            onActivate={() => void model.activate(selected.id)}
            onEdit={() => { model.clearError(); setEditor({ mode: 'edit', provider: selected }) }}
            onDelete={() => { model.clearError(); setRemoveProvider(selected) }}
            onCodexSignIn={signInCodexAgain}
            onCodexDisconnect={beginCodexDisconnect}
            onClose={() => { setOpened(false); setSelectedID('') }}
          />
        ) : narrow ? null : (
          <div className={styles.noSelection}>
            <EmptyState icon={Database} title="No provider selected" hint="Select a provider to inspect its policy." />
          </div>
        )}
      </div>

      {editor ? (
        <ProviderEditor
          mode={editor.mode}
          provider={editor.provider}
          pending={Boolean(state.pendingId)}
          operationError={state.error}
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
          active={removeProvider.id === state.catalog.activeId}
          pending={isCodex(removeProvider) ? removingCodex : Boolean(state.pendingId)}
          error={isCodex(removeProvider) ? logoutErrorCopy(codexState.logoutError) : state.error}
          codexAccountCount={isCodex(removeProvider) ? codexState.accounts.length : 0}
          onCancel={() => setRemoveProvider(null)}
          onConfirm={async () => {
            if (isCodex(removeProvider)) {
              // The preset delete is the sign-out that also removes the
              // entry, so it rides the codex logout with remove for every
              // account at once instead of the generic providers.delete,
              // which refuses presets.
              setRemovingCodex(true)
              const ok = await codexModel.logout(null, true)
              setRemovingCodex(false)
              if (!ok) return
              setSelectedID('')
              setRemoveProvider(null)
              void model.refresh()
              return
            }
            if (await model.delete(removeProvider.id)) {
              setSelectedID('')
              setRemoveProvider(null)
            }
          }}
        />
      ) : null}
      {addProvider !== null ? (
        <AddProviderDialog
          step={addProvider}
          codexProviderId={codexRow?.id ?? null}
          onDismiss={() => setAddProvider(null)}
          onBack={() => setAddProvider('choose')}
          onChoosePreset={() => setAddProvider('codex')}
          onCustom={openCustomEditor}
          onSelectCodexProvider={selectCodexRow}
          onDisconnect={beginCodexDisconnect}
        />
      ) : null}
      {disconnectCodex !== null ? (
        <DisconnectCodexDialog
          email={disconnectCodex.email}
          otherSignedIn={disconnectCodex.otherSignedIn}
          pending={disconnecting}
          error={logoutErrorCopy(codexState.logoutError)}
          onCancel={() => setDisconnectCodex(null)}
          onConfirm={async () => {
            setDisconnecting(true)
            const ok = await codexModel.logout(disconnectCodex.accountId)
            setDisconnecting(false)
            if (!ok) return
            setDisconnectCodex(null)
            // With several accounts the add flow stays open: another
            // account can be added or disconnected right away. The catalog
            // refresh picks up any preset-level change.
            void model.refresh()
          }}
        />
      ) : null}
    </section>
  )
}

function ProviderInspector({ provider, active, pending, codexAccounts, onActivate, onEdit, onDelete, onCodexSignIn, onCodexDisconnect, onClose }: {
  provider: Provider; active: boolean; pending: boolean
  codexAccounts: readonly CodexAccount[]
  onActivate: () => void; onEdit: () => void; onDelete: () => void
  onCodexSignIn: () => void; onCodexDisconnect: (accountId: string) => void; onClose: () => void
}) {
  // Not a modal: no focus trap, no autofocus — Escape and the X simply close it.
  const panelRef = useRef<HTMLElement>(null)
  useEffect(() => {
    const panel = panelRef.current
    if (!panel) return
    const keydown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault()
        onClose()
      }
    }
    panel.addEventListener('keydown', keydown)
    return () => panel.removeEventListener('keydown', keydown)
  }, [onClose])
  // A Codex preset without a signed-in account is the state worth acting
  // on: the header and the action row lead with it instead of the wiring
  // details. The aggregate mirrors the model's: any live account keeps the
  // preset live.
  const codexSignedIn = codexAccounts.some((account) => account.state === 'signed_in')
  const codexNeedsSignIn = isCodex(provider) && provider.enabled && !codexSignedIn
  const codexAggregate = codexSignedIn ? 'signed_in' : codexAccounts.some((account) => account.state === 'reauth_needed') ? 'reauth_needed' : 'signed_out'
  const codexStateDot = codexNeedsSignIn
    ? codexAggregate === 'reauth_needed' ? 'degraded' : 'stopped'
    : provider.enabled ? 'healthy' : 'stopped'
  const codexStateLabel = codexNeedsSignIn
    ? codexAggregate === 'reauth_needed' ? 'Sign-in needed' : 'Signed out'
    : provider.enabled ? 'Configured' : 'Disabled'
  return (
    <aside ref={panelRef} className={styles.inspector} aria-label={`${provider.name} provider details`}>
      <header className={styles.inspectorHeader}>
        <div>
          <h2>{provider.name}</h2>
          <span><StatusDot state={codexStateDot} />{codexStateLabel}</span>
        </div>
        <div className={styles.inspectorHeaderActions}>
          <Button variant={active ? 'secondary' : 'primary'} disabled={active || pending || !provider.enabled} onClick={onActivate}>{pending ? 'Switching…' : active ? 'Active route' : 'Activate'}</Button>
          <button type="button" className={styles.closeInspector} aria-label="Close provider details" onClick={onClose}><X size={16} aria-hidden="true" /></button>
        </div>
      </header>
      <div className={styles.inspectorActions}>
        {codexNeedsSignIn ? (
          <Button variant="secondary" onClick={onCodexSignIn}><LogIn size={14} />Sign in again</Button>
        ) : null}
        {/* The codex preset is managed by the account sign-in, so like the
            manual wiring fields it gets no editor; builtin providers are
            curated configs and keep the button. */}
        {!isCodex(provider) ? (
          <Button variant="secondary" onClick={onEdit}><Pencil size={14} />Edit</Button>
        ) : null}
        {/* Every non-builtin provider is deletable: the cascade deletes its
            keys and routes with it, so neither state parks behind a gate
            here. The codex delete routes through the account sign-in. */}
        {!provider.builtin ? (
          <Button variant="danger" onClick={onDelete}><Trash2 size={14} />Delete</Button>
        ) : null}
      </div>
      <div className={styles.inspectorScroll}>
        <section className={styles.section}><h3>Identity</h3><dl><dt>Display name</dt><dd>{provider.name}</dd>{isCodex(provider) ? <><dt>Preset</dt><dd>Codex — managed by account sign-in</dd></> : null}<dt>Base URL</dt><dd className={styles.endpoint}>{provider.baseUrl}</dd><dt>Dialect</dt><dd>{provider.dialect}</dd><dt>Models path</dt><dd className={styles.endpoint}>{provider.modelsPath}</dd><dt>Request format</dt><dd>{formatLabel(provider.format)}{provider.format === 'chat' ? ` · ${provider.chatPath}` : ''}</dd><dt>Authentication</dt><dd>{provider.authMode === 'custom' ? provider.authHeader : provider.authMode}</dd></dl></section>
        <section className={styles.section}><h3>Traffic policy</h3><dl><dt>Request limit</dt><dd>{rateLabel(provider)}</dd><dt>Prompt cache TTL</dt><dd>{provider.cacheTtl === '1h0m0s' ? '1 hour' : 'Provider default'}</dd><dt>Image API</dt><dd>{provider.imageCompat ? 'Responses tool compatibility' : 'Native provider endpoint'}</dd><dt>Relay behavior</dt><dd>{active ? 'Receives unassigned models' : 'Available for model routes'}</dd></dl></section>
        <section className={styles.section}><h3>Authentication</h3><div className={styles.authRow}>{provider.authMode === 'passthrough' ? <ShieldCheck size={18} /> : <KeyRound size={18} />}<div><strong>{provider.authMode === 'passthrough' ? 'Forwarded from local client' : provider.keyCount > 0 ? `${provider.keyCount} encrypted keys` : 'No API keys configured'}</strong><span>{provider.authMode === 'passthrough' ? 'Keys added in API Keys are used only for model discovery and tests.' : 'Secret material is never returned to the UI.'}</span></div></div></section>
        {isCodex(provider) ? (
          <section className={styles.section}>
            <h3>Codex accounts</h3>
            {codexAccounts.length === 0 ? (
              <p className={styles.accountEmpty}>No accounts are connected. Sign in to add one.</p>
            ) : (
              <div className={styles.accountList}>
                {codexAccounts.map((account) => (
                  <div key={account.accountId} className={styles.accountRow}>
                    <dl>
                      <dt>Email</dt>
                      <dd className={styles.accountEmail} title={account.email !== '' ? account.email : undefined}>{account.email !== '' ? account.email : '—'}</dd>
                      {account.plan !== '' ? <><dt>Plan</dt><dd>{account.plan}</dd></> : null}
                      <dt>State</dt>
                      <dd><span className={styles.accountState}><StatusDot state={account.state === 'signed_in' ? 'healthy' : account.state === 'reauth_needed' ? 'degraded' : 'stopped'} />{account.state === 'signed_in' ? 'Signed in' : account.state === 'reauth_needed' ? 'Sign-in needed' : 'Signed out'}</span></dd>
                    </dl>
                    {/* With one account the plain label says enough; several
                        accounts need the email in the accessible name to
                        tell the disconnect buttons apart. */}
                    {account.state !== 'signed_out' ? (
                      <Button variant="danger" aria-label={codexAccounts.length > 1 ? `Disconnect ${account.email}` : undefined} onClick={() => onCodexDisconnect(account.accountId)}>Disconnect</Button>
                    ) : null}
                  </div>
                ))}
              </div>
            )}
          </section>
        ) : null}
      </div>
    </aside>
  )
}

function formatLabel(format: Provider['format']): string {
  switch (format) {
    case 'chat': return 'Chat completions (auto-translated)'
    case 'responses': return 'Responses API'
    default: return 'Auto-detect'
  }
}

// The list column is narrow, so the unit is abbreviated there and spelled out in
// the inspector. Both have to name it: the same number means a very different
// budget per minute and per second.
function rateShort(provider: Provider): string {
  if (provider.rpm === 0) return '∞'
  return `${provider.rpm}/${provider.rateUnit === 'second' ? 's' : 'min'}`
}

function rateLabel(provider: Provider): string {
  if (provider.rpm === 0) return 'Unlimited'
  return `${provider.rpm} per ${provider.rateUnit === 'second' ? 'second' : 'minute'}`
}

// The backend's refusal sentences (the active-route guard among them) are
// short and user-facing, so they travel verbatim; anything this long is a
// dump (a provider list, an error blob) that has no place in a confirm
// dialog, so it is cut at a word boundary instead.
const LOGOUT_ERROR_MAX = 200
function logoutErrorCopy(error: string): string {
  const trimmed = error.trim()
  if (trimmed.length <= LOGOUT_ERROR_MAX) return trimmed
  const cut = trimmed.slice(0, LOGOUT_ERROR_MAX)
  const boundary = cut.lastIndexOf(' ')
  return `${boundary > 0 ? cut.slice(0, boundary) : cut}…`
}

function ProviderEditor({ mode, provider, pending, operationError, active, onClose, onSubmit }: {
  mode: 'add' | 'edit'; provider?: Provider; pending: boolean; active: boolean
  operationError: string
  onClose: () => void; onSubmit: (value: ProviderInput) => Promise<void>
}) {
  const [name, setName] = useState(provider?.name ?? '')
  const [baseUrl, setBaseURL] = useState(provider?.baseUrl ?? '')
  const [authMode, setAuthMode] = useState<Provider['authMode']>(provider?.authMode ?? 'bearer')
  const [authHeader, setAuthHeader] = useState(provider?.authHeader ?? '')
  const [dialect, setDialect] = useState<Provider['dialect']>(provider?.dialect ?? 'auto')
  const [modelsPath, setModelsPath] = useState(provider?.modelsPath ?? '/v1/models')
  const [format, setFormat] = useState<Provider['format']>(provider?.format ?? 'auto')
  const [chatPath, setChatPath] = useState(provider?.chatPath ?? '/v1/chat/completions')
  const [imageCompat, setImageCompat] = useState(provider?.imageCompat ?? false)
  const [rpm, setRPM] = useState(String(provider?.rpm ?? 0))
  const [rateUnit, setRateUnit] = useState<RateUnit>(provider?.rateUnit === 'second' ? 'second' : 'minute')
  const [cache1h, setCache1H] = useState(provider?.cacheTtl === '1h0m0s')
  const [enabled, setEnabled] = useState(provider?.enabled ?? true)
  const [error, setError] = useState('')
  const dialogRef = useModalFocus<HTMLFormElement>(onClose, pending)

  const submit = (event: FormEvent) => {
    event.preventDefault()
    const parsedRPM = Number(rpm)
    const value = { name: name.trim(), baseUrl: baseUrl.trim(), authMode, authHeader: authHeader.trim(), dialect, modelsPath: modelsPath.trim(), format, chatPath: chatPath.trim(), imageCompat, rpm: parsedRPM, rateUnit, cache1h, enabled }
    const validationError = providerInputError(value)
    if (validationError || (active && !enabled)) {
      setError(active && !enabled ? 'Switch away from the provider before disabling it.' : validationError)
      return
    }
    setError('')
    void onSubmit(value)
  }

  return (
    <div className="ui-scrim" onMouseDown={(event) => { if (event.target === event.currentTarget && !pending) onClose() }}>
      <form ref={dialogRef} className={`ui-modal ${styles.providerModal}`} role="dialog" aria-modal="true" aria-label={mode === 'add' ? 'Add provider' : 'Edit provider'} onSubmit={submit}>
        <header><div><h2>{mode === 'add' ? 'Add provider' : `Edit ${provider?.name}`}</h2><p>Remote endpoints require HTTPS; loopback HTTP is allowed.</p></div><button type="button" aria-label="Close" disabled={pending} onClick={onClose}><X size={18} /></button></header>
        <div className={styles.formBody}>
          <label><span>Display name</span><input value={name} maxLength={80} data-autofocus onChange={(event) => setName(event.currentTarget.value)} /></label>
          <label><span>Base URL</span><input type="url" className={styles.monoInput} value={baseUrl} maxLength={2048} required placeholder="https://provider.example/v1" onChange={(event) => setBaseURL(event.currentTarget.value)} /></label>
          <label><span>API dialect</span><select value={dialect} onChange={(event) => setDialect(event.currentTarget.value as Provider['dialect'])}><option value="auto">Auto-detect from request</option><option value="openai">OpenAI compatible</option><option value="anthropic">Anthropic compatible</option></select></label>
          <label><span>Models discovery path</span><input className={styles.monoInput} value={modelsPath} maxLength={160} required placeholder="/v1/models" onChange={(event) => setModelsPath(event.currentTarget.value)} /></label>
          <label><span>Request format</span><select value={format} onChange={(event) => setFormat(event.currentTarget.value as Provider['format'])}><option value="auto">Auto-detect (Responses first)</option><option value="responses">Responses API only</option><option value="chat">Chat completions only (translated)</option></select><small>Chat completions providers receive Responses API calls automatically.</small></label>
          {format === 'chat' ? <label><span>Chat completions path</span><input className={styles.monoInput} value={chatPath} maxLength={160} required placeholder="/v1/chat/completions" onChange={(event) => setChatPath(event.currentTarget.value)} /><small>Where the provider actually serves chat completions.</small></label> : null}
          <label className={styles.check}><input type="checkbox" checked={imageCompat} onChange={(event) => setImageCompat(event.currentTarget.checked)} />Bridge image generation through the Responses image tool</label>
          <label><span>Authentication</span><select value={authMode} onChange={(event) => setAuthMode(event.currentTarget.value as Provider['authMode'])}><option value="auto">Auto by API dialect</option><option value="bearer">Bearer token</option><option value="x-api-key">x-api-key</option><option value="custom">Custom header</option><option value="passthrough">Pass through client auth</option></select></label>
          {authMode === 'custom' ? <label><span>Custom auth header</span><input value={authHeader} maxLength={64} required placeholder="Authorization or api-key" onChange={(event) => setAuthHeader(event.currentTarget.value)} /><small>The encrypted key value is sent exactly as stored, including an optional Token or Basic prefix.</small></label> : null}
          <div className={styles.ratePair}>
            <label><span>Request limit</span><input type="number" min="0" max="1000000" step="1" required value={rpm} onChange={(event) => setRPM(event.currentTarget.value)} /></label>
            <label><span>Counted per</span><select value={rateUnit} onChange={(event) => setRateUnit(event.currentTarget.value as RateUnit)}><option value="minute">Minute</option><option value="second">Second</option></select></label>
            <small>0 means unlimited. Per-key RPM still applies. Choose seconds for providers that cap bursts, such as 5 requests per second.</small>
          </div>
          <label className={styles.check}><input type="checkbox" checked={cache1h} onChange={(event) => setCache1H(event.currentTarget.checked)} />Extend existing ephemeral cache controls to 1 hour</label>
          <label className={styles.check}><input type="checkbox" checked={enabled} disabled={active} onChange={(event) => setEnabled(event.currentTarget.checked)} />Provider enabled</label>
          {error || operationError ? <p className={styles.formError} role="alert">{error || operationError}</p> : null}
        </div>
        <footer><Button type="button" disabled={pending} onClick={onClose}>Cancel</Button><Button type="submit" variant="primary" disabled={pending}>{pending ? 'Saving…' : 'Save provider'}</Button></footer>
      </form>
    </div>
  )
}

function ConfirmDelete({ provider, active, pending, error, codexAccountCount = 0, onCancel, onConfirm }: { provider: Provider; active: boolean; pending: boolean; error: string; codexAccountCount?: number; onCancel: () => void; onConfirm: () => Promise<void> }) {
  const dialogRef = useModalFocus<HTMLElement>(onCancel, pending)
  // The delete is a cascade: the provider's keys and the routes that use it
  // go with it, and an active route moves to a built-in in the same save, so
  // the dialog names exactly what disappears instead of demanding cleanup.
  // The codex variant signs out every account, and with several of them the
  // scope deserves saying.
  const body = isCodex(provider)
    ? `${codexAccountCount > 1 ? `All ${codexAccountCount} accounts are` : 'The account is'} signed out and the preset entry is deleted along with its model routes.${active ? ' The active route moves to an enabled built-in provider.' : ''} You can connect again at any time.`
    : `${provider.keyCount > 0 ? `Its ${provider.keyCount} API ${provider.keyCount === 1 ? 'key' : 'keys'} and ` : ''}the model routes that use this provider are deleted with it${active ? '. The active route moves to an enabled built-in provider.' : '.'}`
  return <div className="ui-scrim"><section ref={dialogRef} className={`ui-modal ${styles.confirm}`} role="dialog" aria-modal="true" aria-label="Delete provider"><header><div><h2>Delete “{provider.name}”?</h2><p>{body}</p></div></header>{error ? <p className={styles.confirmError} role="alert">{error}</p> : null}<footer><Button disabled={pending} onClick={onCancel}>Cancel</Button><Button variant="danger" disabled={pending} onClick={() => void onConfirm()}>{pending ? 'Deleting…' : 'Delete provider'}</Button></footer></section></div>
}
