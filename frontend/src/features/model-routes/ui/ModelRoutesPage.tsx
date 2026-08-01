import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { CheckCheck, FlaskConical, Pencil, Plus, RefreshCw, Search, Trash2, X } from 'lucide-react'
import { formatDuration } from '../../../shared/format/metrics'
import { Button } from '../../../shared/ui/Button'
import { StatusDot } from '../../../shared/ui/StatusDot'
import { useModalFocus } from '../../../shared/ui/useModalFocus'
import { useModels } from '../../models/ui/useModels'
import { useProviders } from '../../providers/ui/useProviders'
import type { ModelRoute, RouteTarget } from '../domain/route'
import { useRoutes } from './useRoutes'
import styles from './ModelRoutesPage.module.css'

export default function ModelRoutesPage() {
  const { model, state } = useRoutes()
  const { model: modelsModel, state: models } = useModels()
  const { state: providers } = useProviders()
  const [editor, setEditor] = useState<ModelRoute | 'new' | null>(null)
  const [search, setSearch] = useState('')
  const defaultProvider = providers.catalog.activeId || providers.catalog.providers[0]?.id || ''

  useEffect(() => { if (state.phase === 'idle') void model.load('relay') }, [model, state.phase])
  useEffect(() => { if (models.phase === 'idle' && defaultProvider) void modelsModel.discover(defaultProvider) }, [defaultProvider, models.phase, modelsModel])

  const visibleModels = useMemo(() => {
    const query = search.trim().toLocaleLowerCase()
    return query ? models.models.filter((item) => item.toLocaleLowerCase().includes(query)) : models.models
  }, [models.models, search])
  const selectedModels = useMemo(() => new Set(models.selected), [models.selected])
  const providerNames = useMemo(() => new Map(providers.catalog.providers.map((provider) => [provider.id, provider.name])), [providers.catalog.providers])
  const allSelected = models.models.length > 0 && models.selected.length === models.models.length
  const bulkRoutes = models.selected.map((upstreamModel) => ({
    target: state.target,
    publicModel: upstreamModel,
    upstreamModel,
    providerId: models.providerId,
    contextLimitKiB: 0,
    enabled: true,
  } satisfies ModelRoute))

  return (
    <section className={styles.page}>
      <header className="page-header">
        <div><h1>Model Routes</h1><p>Discover, test and assign models independently for Relay and Tunnel</p></div>
        <Button variant="primary" onClick={() => setEditor('new')}><Plus size={16} />Add route</Button>
      </header>

      <div className={styles.toolbar}>
        <div className={styles.targets}>
          <button type="button" data-active={state.target === 'relay'} onClick={() => void model.load('relay')}>Local Relay</button>
          <button type="button" data-active={state.target === 'tunnel'} onClick={() => void model.load('tunnel')}>Public Tunnel</button>
        </div>
        <span>{state.target === 'relay' ? 'Unassigned models use the active provider.' : 'Only enabled aliases in this list appear in /v1/models.'}</span>
      </div>

      {(state.error || models.error) ? <div className={styles.error} role="alert">{state.error || models.error}</div> : null}

      <section className={styles.catalog}>
        <header>
          <div><h2>Provider models</h2><p>{models.models.length} discovered · {models.selected.length} selected</p></div>
          <div className={styles.catalogActions}>
            <label className={styles.providerSelect}><span>Provider</span><select value={models.providerId || defaultProvider} onChange={(event) => void modelsModel.discover(event.currentTarget.value)}>{providers.catalog.providers.filter((provider) => provider.enabled).map((provider) => <option key={provider.id} value={provider.id}>{provider.name}</option>)}</select></label>
            <Button disabled={!models.providerId || models.phase === 'loading' || models.testing} onClick={() => void modelsModel.discover(models.providerId)}><RefreshCw size={14} />{models.phase === 'loading' ? 'Loading…' : 'Refresh'}</Button>
          </div>
        </header>
        <div className={styles.catalogToolbar}>
          <label className={styles.search}><Search size={15} /><input type="search" value={search} placeholder="Filter models" onChange={(event) => setSearch(event.currentTarget.value)} /></label>
          <Button disabled={models.models.length === 0 || models.testing} onClick={() => modelsModel.toggleAll()}><CheckCheck size={15} />{allSelected ? 'Clear all' : 'All models'}</Button>
          <Button disabled={models.selected.length === 0 || models.testing} onClick={() => void modelsModel.test(models.selected)}><FlaskConical size={15} />Test selected</Button>
          <Button disabled={models.models.length === 0} onClick={() => models.testing ? modelsModel.cancelTest() : void modelsModel.test(models.models)}>{models.testing ? 'Cancel tests' : 'Test all'}</Button>
          <Button variant="primary" disabled={bulkRoutes.length === 0 || Boolean(state.pending)} onClick={() => void model.upsertMany(bulkRoutes)}>{state.target === 'tunnel' ? 'Publish selected' : 'Route selected'}</Button>
        </div>
        <div className={styles.modelList}>
          {visibleModels.map((item) => {
            const result = models.results[item]
            return <label key={item} className={styles.modelRow} data-selected={selectedModels.has(item)}><input type="checkbox" checked={selectedModels.has(item)} onChange={() => modelsModel.toggle(item)} /><span title={item}>{item}</span><ModelResult result={result} /></label>
          })}
          {models.phase === 'ready' && visibleModels.length === 0 ? <div className={styles.noModels}>No models match this filter.</div> : null}
          {models.phase === 'loading' ? <div className={styles.noModels}>Loading provider catalog…</div> : null}
        </div>
      </section>

      <section className={styles.routes}>
        <header><div><h2>{state.target === 'relay' ? 'Relay assignments' : 'Published tunnel models'}</h2><p>{state.routes.length} routes</p></div></header>
        <div className={styles.tableWrap}><table className={styles.table}><thead><tr><th>Public model / alias</th><th>Upstream model</th><th>Provider</th><th>Context cap</th><th>State</th><th>Actions</th></tr></thead><tbody>
          {state.routes.map((route) => <tr key={route.publicModel}><td><strong>{route.publicModel}</strong></td><td>{route.upstreamModel}</td><td>{providerNames.get(route.providerId) ?? route.providerId}</td><td>{route.contextLimitKiB === 0 ? 'Default' : `${Math.round(route.contextLimitKiB / 1024)} MiB`}</td><td>{route.enabled ? 'Enabled' : 'Disabled'}</td><td><div className={styles.actions}><button type="button" aria-label={`Edit ${route.publicModel}`} onClick={() => setEditor(route)}><Pencil /></button><button type="button" aria-label={`Delete ${route.publicModel}`} onClick={() => void model.delete(route.publicModel)}><Trash2 /></button></div></td></tr>)}
          {state.phase !== 'loading' && state.routes.length === 0 ? <tr><td colSpan={6} className={styles.empty}>No {state.target === 'relay' ? 'relay assignments' : 'public tunnel models'} yet.</td></tr> : null}
        </tbody></table></div>
      </section>

      {editor ? <RouteEditor target={state.target} route={editor === 'new' ? undefined : editor} providers={providers.catalog.providers.filter((provider) => provider.enabled)} pending={Boolean(state.pending)} onClose={() => setEditor(null)} onSave={async route => { if (await model.upsert(route)) setEditor(null) }} /> : null}
    </section>
  )
}

function ModelResult({ result }: { result?: ReturnType<typeof useModels>['state']['results'][string] }) {
  if (!result) return <small>Not tested</small>
  const state = result.state === 'available' ? 'completed' : result.state === 'testing' ? 'active' : 'failed'
  return <small className={styles.testResult}><StatusDot state={state} />{result.state === 'testing' ? 'Testing' : result.state === 'available' ? `${formatDuration(result.latencyMs)} · ${result.status}` : `${result.errorCode || 'Unavailable'} · ${result.status || '—'}`}</small>
}

function RouteEditor({ target, route, providers, pending, onClose, onSave }: { target: RouteTarget; route?: ModelRoute; providers: readonly { id: string; name: string }[]; pending: boolean; onClose: () => void; onSave: (route: ModelRoute) => Promise<void> }) {
  const [publicModel, setPublicModel] = useState(route?.publicModel ?? '')
  const [upstreamModel, setUpstreamModel] = useState(route?.upstreamModel ?? '')
  const [providerId, setProviderId] = useState(providers.some((provider) => provider.id === route?.providerId) ? route?.providerId ?? '' : providers[0]?.id ?? '')
  const [contextMiB, setContextMiB] = useState(String((route?.contextLimitKiB ?? 0) / 1024))
  const [enabled, setEnabled] = useState(route?.enabled ?? true)
  const [error, setError] = useState('')
  const dialogRef = useModalFocus<HTMLFormElement>(onClose, pending)
  const submit = (event: FormEvent) => {
    event.preventDefault()
    const context = Number(contextMiB)
    if (!publicModel.trim() || !upstreamModel.trim() || !providerId || !Number.isFinite(context) || context < 0 || context > 2_048) { setError('Complete both model names, provider and a context limit from 0 to 2048 MiB.'); return }
    setError('')
    void onSave({ target, publicModel: publicModel.trim(), upstreamModel: upstreamModel.trim(), providerId, contextLimitKiB: Math.round(context * 1024), enabled })
  }
  return <div className="ui-scrim"><form ref={dialogRef} className={`ui-modal ${styles.routeModal}`} role="dialog" aria-modal="true" aria-label={route ? 'Edit model route' : 'Add model route'} onSubmit={submit}><header><div><h2>{route ? 'Edit route' : 'Add route'}</h2><p>{target === 'tunnel' ? 'Public alias never exposes the upstream model or provider.' : 'Requested model is routed to the selected provider.'}</p></div><button type="button" aria-label="Close" onClick={onClose}><X /></button></header><div className={styles.form}><label><span>{target === 'tunnel' ? 'Public alias' : 'Requested model'}</span><input value={publicModel} maxLength={128} required disabled={Boolean(route)} autoFocus onChange={event => setPublicModel(event.currentTarget.value)} /></label><label><span>Upstream model</span><input value={upstreamModel} maxLength={128} required onChange={event => setUpstreamModel(event.currentTarget.value)} /></label><label><span>Provider</span><select value={providerId} required onChange={event => setProviderId(event.currentTarget.value)}>{providers.map(provider => <option key={provider.id} value={provider.id}>{provider.name}</option>)}</select></label><label><span>Context limit</span><span className={styles.suffixed}><input type="number" min="0" max="2048" step="0.001" required value={contextMiB} onChange={event => setContextMiB(event.currentTarget.value)} /><small>MiB</small></span></label><label className={styles.check}><input type="checkbox" checked={enabled} onChange={event => setEnabled(event.currentTarget.checked)} />Route enabled</label>{error ? <p className={styles.formError} role="alert">{error}</p> : null}</div><footer><Button type="button" onClick={onClose}>Cancel</Button><Button type="submit" variant="primary" disabled={pending}>{pending ? 'Saving…' : 'Save route'}</Button></footer></form></div>
}
