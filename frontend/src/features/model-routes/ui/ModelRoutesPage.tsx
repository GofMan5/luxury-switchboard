import { memo, useCallback, useDeferredValue, useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { CheckCheck, FlaskConical, Pencil, Plus, RefreshCw, Search, Trash2, X } from 'lucide-react'
import { formatDuration } from '../../../shared/format/metrics'
import { Button } from '../../../shared/ui/Button'
import { StatusDot } from '../../../shared/ui/StatusDot'
import { useModalFocus } from '../../../shared/ui/useModalFocus'
import { OWNER_EDITION } from '../../../app/edition'
import { useModels } from '../../models/ui/useModels'
import { useProviders } from '../../providers/ui/useProviders'
import { publishedModels, selectionChanges, type ModelRoute, type RouteTarget } from '../domain/route'
import { useRoutes } from './useRoutes'
import styles from './ModelRoutesPage.module.css'

const MODEL_RENDER_BATCH = 180

type CatalogFilter = 'all' | 'published' | 'unpublished' | 'failed'

const FILTERS: readonly { id: CatalogFilter; label: string }[] = [
  { id: 'all', label: 'All' },
  { id: 'published', label: 'Published' },
  { id: 'unpublished', label: 'Not published' },
  { id: 'failed', label: 'Failed test' },
]

type ModelPublication = { relay: string; tunnel: string }

export default function ModelRoutesPage() {
  const { model, state } = useRoutes()
  const { model: modelsModel, state: models } = useModels()
  const { state: providers } = useProviders()
  const [editor, setEditor] = useState<ModelRoute | 'new' | null>(null)
  const [search, setSearch] = useState('')
  const [filter, setFilter] = useState<CatalogFilter>('all')
  const deferredSearch = useDeferredValue(search.trim().toLocaleLowerCase())
  const [modelWindow, setModelWindow] = useState(() => ({ catalog: models.models, query: '', filter, limit: MODEL_RENDER_BATCH }))
  const defaultProvider = providers.catalog.activeId || providers.catalog.providers[0]?.id || ''
  const selectedProviderAvailable = providers.catalog.providers.some((provider) => provider.enabled && provider.id === models.providerId)

  useEffect(() => { if (state.phase === 'idle') void model.load('relay') }, [model, state.phase])
  useEffect(() => {
    if (defaultProvider && (models.phase === 'idle' || !selectedProviderAvailable)) void modelsModel.discover(defaultProvider)
  }, [defaultProvider, models.phase, modelsModel, selectedProviderAvailable])

  const publication = useMemo(() => {
    const map = new Map<string, ModelPublication>()
    for (const target of ['relay', 'tunnel'] as const) {
      for (const route of state.published[target]) {
        if (route.providerId !== models.providerId) continue
        const current = map.get(route.upstreamModel) ?? { relay: '', tunnel: '' }
        map.set(route.upstreamModel, { ...current, [target]: route.publicModel })
      }
    }
    return map
  }, [models.providerId, state.published])
  const publishedHere = useMemo(() => publishedModels(state.routes, models.providerId), [models.providerId, state.routes])

  // The catalog selection mirrors what is published, so one apply step is enough
  // and the user always sees the live routing state instead of an empty list.
  const seedKey = `${state.target}|${models.providerId}|${models.models.length}|${publishedHere.join(' ')}`
  const seeded = useRef('')
  useEffect(() => {
    if (models.phase !== 'ready' || seeded.current === seedKey) return
    seeded.current = seedKey
    modelsModel.select(publishedHere)
  }, [modelsModel, models.phase, publishedHere, seedKey])

  const selectedModels = useMemo(() => new Set(models.selected), [models.selected])
  const visibleModels = useMemo(() => models.models.filter((item) => {
    if (deferredSearch && !item.toLocaleLowerCase().includes(deferredSearch)) return false
    const routed = publication.get(item)
    switch (filter) {
      case 'published': return Boolean(state.target === 'relay' ? routed?.relay : routed?.tunnel)
      case 'unpublished': return !(state.target === 'relay' ? routed?.relay : routed?.tunnel)
      case 'failed': return models.results[item]?.state === 'unavailable'
      default: return true
    }
  }), [deferredSearch, filter, models.models, models.results, publication, state.target])
  const visibleSet = useMemo(() => new Set(visibleModels), [visibleModels])

  const modelWindowMatches = modelWindow.catalog === models.models && modelWindow.query === deferredSearch && modelWindow.filter === filter
  const modelLimit = modelWindowMatches ? modelWindow.limit : MODEL_RENDER_BATCH
  const renderedModels = visibleModels.slice(0, modelLimit)
  const remainingModels = visibleModels.length - renderedModels.length
  const nextModelBatch = Math.min(MODEL_RENDER_BATCH, remainingModels)
  const showMoreModels = () => setModelWindow({ catalog: models.models, query: deferredSearch, filter, limit: modelLimit + MODEL_RENDER_BATCH })
  const providerNames = useMemo(() => new Map(providers.catalog.providers.map((provider) => [provider.id, provider.name])), [providers.catalog.providers])
  const toggleModel = useCallback((item: string) => modelsModel.toggle(item), [modelsModel])
  const visibleSelected = visibleModels.filter((item) => selectedModels.has(item)).length
  const selectShown = () => modelsModel.select([...new Set([...models.selected, ...visibleModels])])
  const clearShown = () => modelsModel.select(models.selected.filter((item) => !visibleSet.has(item)))
  const changes = useMemo(
    () => selectionChanges(state.routes, state.target, models.providerId, models.selected),
    [models.providerId, models.selected, state.routes, state.target],
  )
  const pendingChanges = changes.additions.length + changes.removals.length
  const busy = Boolean(state.pending) || models.testing || !models.providerId

  return (
    <section className={styles.page}>
      <header className="page-header">
        <div><h1>Model Routes</h1><p>Pick the models each target serves; aliases stay editable per route</p></div>
        <Button variant="primary" onClick={() => { model.clearError(); setEditor('new') }}><Plus size={16} />Add route</Button>
      </header>

      {OWNER_EDITION ? (
        <div className={styles.toolbar}>
          <div className={styles.targets}>
            <button type="button" data-active={state.target === 'relay'} onClick={() => void model.load('relay')}>Local Relay</button>
            <button type="button" data-active={state.target === 'tunnel'} onClick={() => void model.load('tunnel')}>Public Tunnel</button>
          </div>
          <span>{state.target === 'relay' ? 'Unassigned models use the active provider.' : 'Only enabled aliases in this list appear in /v1/models.'}</span>
        </div>
      ) : null}

      {(state.error || models.error) ? <div className={styles.error} role="alert">{state.error || models.error}</div> : null}

      <section className={styles.catalog}>
        <header>
          <div>
            <h2>Provider models</h2>
            <p>{models.models.length} discovered · {publishedHere.length} on {state.target === 'relay' ? 'relay' : 'tunnel'} · {models.selected.length} selected</p>
          </div>
          <div className={styles.catalogActions}>
            <label className={styles.providerSelect}><span>Provider</span><select value={models.providerId || defaultProvider} onChange={(event) => void modelsModel.discover(event.currentTarget.value)}>{providers.catalog.providers.filter((provider) => provider.enabled).map((provider) => <option key={provider.id} value={provider.id}>{provider.name}</option>)}</select></label>
            <Button disabled={!models.providerId || models.phase === 'loading' || models.testing} onClick={() => void modelsModel.discover(models.providerId)}><RefreshCw size={14} />{models.phase === 'loading' ? 'Loading…' : 'Refresh'}</Button>
          </div>
        </header>
        <div className={styles.catalogToolbar}>
          <label className={styles.search}><Search size={15} /><input type="search" value={search} placeholder="Filter models" onChange={(event) => setSearch(event.currentTarget.value)} /></label>
          <div className={styles.filters} role="group" aria-label="Catalog filter">
            {FILTERS.map((entry) => <button key={entry.id} type="button" data-active={filter === entry.id} onClick={() => setFilter(entry.id)}>{entry.label}</button>)}
          </div>
          <Button disabled={visibleModels.length === 0 || models.testing} onClick={() => visibleSelected === visibleModels.length ? clearShown() : selectShown()}>
            <CheckCheck size={15} />{visibleSelected === visibleModels.length && visibleModels.length > 0 ? 'Clear shown' : 'Select shown'}
          </Button>
          <Button disabled={models.selected.length === 0 || models.testing} onClick={() => void modelsModel.test(models.selected)}><FlaskConical size={15} />Test selected</Button>
          <Button disabled={models.models.length === 0} onClick={() => models.testing ? modelsModel.cancelTest() : void modelsModel.test(visibleModels)}>{models.testing ? 'Cancel tests' : 'Test shown'}</Button>
        </div>
        <div className={styles.modelList}>
          {renderedModels.map((item) => (
            <ModelCatalogRow
              key={item}
              item={item}
              selected={selectedModels.has(item)}
              routed={publication.get(item)}
              result={models.results[item]}
              onToggle={toggleModel}
            />
          ))}
          {models.phase === 'ready' && visibleModels.length === 0 ? <div className={styles.noModels}>No models match this filter.</div> : null}
          {models.phase === 'loading' ? <div className={styles.noModels}>Loading provider catalog…</div> : null}
        </div>
        <footer className={styles.modelWindow} aria-live="polite">
          <span>{visibleModels.length > 0 ? `Showing ${renderedModels.length} of ${visibleModels.length} models` : 'Nothing to show'}</span>
          <div className={styles.windowActions}>
            {remainingModels > 0 ? <Button type="button" onClick={showMoreModels}>Show {nextModelBatch} more</Button> : null}
            <span className={styles.diff} data-dirty={pendingChanges > 0 || undefined}>
              {pendingChanges === 0 ? 'Selection matches the published routes' : `+${changes.additions.length} new · −${changes.removals.length} removed`}
            </span>
            <Button variant="primary" disabled={busy || pendingChanges === 0} onClick={() => void model.applySelection(models.providerId, models.selected)}>
              {state.pending ? 'Applying…' : state.target === 'tunnel' ? 'Apply to tunnel' : 'Apply to relay'}
            </Button>
          </div>
        </footer>
      </section>

      <section className={styles.routes}>
        <header><div><h2>{state.target === 'relay' ? 'Relay assignments' : 'Published tunnel models'}</h2><p>{state.routes.length} routes</p></div></header>
        <div className={styles.tableWrap}><table className={styles.table}><thead><tr><th>Public model / alias</th><th>Upstream model</th><th>Provider</th><th>Context cap</th><th>State</th><th>Actions</th></tr></thead><tbody>
          {state.routes.map((route) => <tr key={route.publicModel}><td><strong>{route.publicModel}</strong></td><td>{route.upstreamModel}</td><td>{providerNames.get(route.providerId) ?? route.providerId}</td><td>{route.contextLimitKiB === 0 ? 'Default' : `${Math.round(route.contextLimitKiB / 1024)} MiB`}</td><td>{route.enabled ? 'Enabled' : 'Disabled'}</td><td><div className={styles.actions}><button type="button" aria-label={`Edit ${route.publicModel}`} onClick={() => { model.clearError(); setEditor(route) }}><Pencil /></button><button type="button" aria-label={`Delete ${route.publicModel}`} onClick={() => void model.delete(route.publicModel)}><Trash2 /></button></div></td></tr>)}
          {state.phase !== 'loading' && state.routes.length === 0 ? <tr><td colSpan={6} className={styles.empty}>No {state.target === 'relay' ? 'relay assignments' : 'public tunnel models'} yet.</td></tr> : null}
        </tbody></table></div>
      </section>

      {editor ? <RouteEditor target={state.target} route={editor === 'new' ? undefined : editor} providers={providers.catalog.providers.filter((provider) => provider.enabled)} pending={Boolean(state.pending)} operationError={state.error} onClose={() => setEditor(null)} onSave={async route => { if (await model.upsert(route)) setEditor(null) }} /> : null}
    </section>
  )
}

type ModelResultValue = ReturnType<typeof useModels>['state']['results'][string]

const ModelCatalogRow = memo(function ModelCatalogRow({ item, selected, routed, result, onToggle }: { item: string; selected: boolean; routed?: ModelPublication; result?: ModelResultValue; onToggle: (item: string) => void }) {
  return (
    <label className={styles.modelRow} data-selected={selected}>
      <input type="checkbox" checked={selected} onChange={() => onToggle(item)} />
      <span title={item}>{item}</span>
      <span className={styles.rowMeta}>
        {routed?.relay ? <em className={styles.badge} data-target="relay" title={`Relay: ${routed.relay}`}>Relay</em> : null}
        {OWNER_EDITION && routed?.tunnel ? <em className={styles.badge} data-target="tunnel" title={`Tunnel alias: ${routed.tunnel}`}>{routed.tunnel === item ? 'Tunnel' : `Tunnel · ${routed.tunnel}`}</em> : null}
        <ModelResult result={result} />
      </span>
    </label>
  )
})

function ModelResult({ result }: { result?: ModelResultValue }) {
  if (!result) return <small>Not tested</small>
  const state = result.state === 'available' ? 'completed' : result.state === 'testing' ? 'active' : 'failed'
  return <small className={styles.testResult}><StatusDot state={state} />{result.state === 'testing' ? 'Testing' : result.state === 'available' ? `${formatDuration(result.latencyMs)} · ${result.status}` : `${result.errorCode || 'Unavailable'} · ${result.status || '—'}`}</small>
}

function RouteEditor({ target, route, providers, pending, operationError, onClose, onSave }: { target: RouteTarget; route?: ModelRoute; providers: readonly { id: string; name: string }[]; pending: boolean; operationError: string; onClose: () => void; onSave: (route: ModelRoute) => Promise<void> }) {
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
  return <div className="ui-scrim"><form ref={dialogRef} className={`ui-modal ${styles.routeModal}`} role="dialog" aria-modal="true" aria-label={route ? 'Edit model route' : 'Add model route'} onSubmit={submit}><header><div><h2>{route ? 'Edit route' : 'Add route'}</h2><p>{target === 'tunnel' ? 'Public alias never exposes the upstream model or provider.' : 'Requested model is routed to the selected provider.'}</p></div><button type="button" aria-label="Close" onClick={onClose}><X /></button></header><div className={styles.form}><label><span>{target === 'tunnel' ? 'Public alias' : 'Requested model'}</span><input value={publicModel} maxLength={128} required disabled={Boolean(route)} data-autofocus onChange={event => setPublicModel(event.currentTarget.value)} /></label><label><span>Upstream model</span><input value={upstreamModel} maxLength={128} required onChange={event => setUpstreamModel(event.currentTarget.value)} /></label><label><span>Provider</span><select value={providerId} required onChange={event => setProviderId(event.currentTarget.value)}>{providers.map(provider => <option key={provider.id} value={provider.id}>{provider.name}</option>)}</select></label><label><span>Context limit</span><span className={styles.suffixed}><input type="number" min="0" max="2048" step="0.001" required value={contextMiB} onChange={event => setContextMiB(event.currentTarget.value)} /><small>MiB</small></span></label><label className={styles.check}><input type="checkbox" checked={enabled} onChange={event => setEnabled(event.currentTarget.checked)} />Route enabled</label>{error || operationError ? <p className={styles.formError} role="alert">{error || operationError}</p> : null}</div><footer><Button type="button" onClick={onClose}>Cancel</Button><Button type="submit" variant="primary" disabled={pending}>{pending ? 'Saving…' : 'Save route'}</Button></footer></form></div>
}
