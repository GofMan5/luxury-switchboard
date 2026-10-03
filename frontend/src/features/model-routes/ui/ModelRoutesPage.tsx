import { memo, useCallback, useDeferredValue, useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { ArrowDown, ArrowUp, CheckCheck, FlaskConical, GitBranch, Pencil, Plus, RefreshCw, Search, Trash2, X } from 'lucide-react'
import { formatDuration } from '../../../shared/format/metrics'
import { Button } from '../../../shared/ui/Button'
import { StatusDot } from '../../../shared/ui/StatusDot'
import { useModalFocus } from '../../../shared/ui/useModalFocus'
import { Segmented } from '../../../shared/ui/chrome'
import { useModels } from '../../models/ui/useModels'
import { useProviders } from '../../providers/ui/useProviders'
import { publishedModels, selectionChanges, type ModelRoute, type RouteTarget } from '../domain/route'
import { useRoutes } from './useRoutes'
import { addChainEntry, moveChainEntry, patchChainEntry, removeChainEntry, seedChainEntries, type ChainEntry, type ChainEntryPatch } from './chain-entries'
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
  const [wizard, setWizard] = useState(false)
  const [search, setSearch] = useState('')
  const [filter, setFilter] = useState<CatalogFilter>('all')
  const deferredSearch = useDeferredValue(search.trim().toLocaleLowerCase())
  const [modelWindow, setModelWindow] = useState(() => ({ catalog: models.models, query: '', filter, limit: MODEL_RENDER_BATCH }))
  const defaultProvider = providers.catalog.activeId || providers.catalog.providers[0]?.id || ''

  useEffect(() => { if (state.phase === 'idle') void model.load('relay') }, [model, state.phase])
  const autoDiscovered = useRef('')
  useEffect(() => {
    // Auto-discovery answers one question once per provider: what the default
    // provider serves. It used to re-fire on every phase change while the
    // current provider was unavailable, so a fallback provider outside the
    // enabled set looped discover against its own abort — polling with no
    // backoff and a catalog that never settled. A failed discovery is the
    // Refresh button's job, not this effect's.
    if (!defaultProvider || autoDiscovered.current === defaultProvider) return
    if (models.phase !== 'idle' && models.providerId === defaultProvider) return
    autoDiscovered.current = defaultProvider
    void modelsModel.discover(defaultProvider)
  }, [defaultProvider, models.phase, models.providerId, modelsModel])

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
  // A discovery that has not finished yet clears the seed: re-discovering the
  // same unchanged catalog produces the same key, and without the reset the
  // checkboxes stayed empty after a plain Refresh — with "Apply to relay" armed
  // to delete everything the seed was supposed to mirror.
  const seedKey = `${state.target}|${models.providerId}|${models.models.length}|${publishedHere.join(' ')}`
  const seeded = useRef('')
  useEffect(() => {
    if (models.phase !== 'ready') {
      seeded.current = ''
      return
    }
    if (seeded.current === seedKey) return
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
  // The table lists chains in failover order: public model first, then the
  // priority that decides which provider answers before which.
  const orderedRoutes = useMemo(
    () => [...state.routes].sort((left, right) => {
      if (left.publicModel !== right.publicModel) return left.publicModel.localeCompare(right.publicModel)
      return (left.priority ?? 0) - (right.priority ?? 0)
    }),
    [state.routes],
  )
  const chainCount = useMemo(() => {
    const sizes = new Map<string, number>()
    for (const route of state.routes) {
      if (state.target === 'relay' && route.enabled) sizes.set(route.publicModel, (sizes.get(route.publicModel) ?? 0) + 1)
    }
    let chained = 0
    for (const size of sizes.values()) if (size > 1) chained++
    return chained
  }, [state.routes, state.target])
  const pendingChanges = changes.additions.length + changes.removals.length
  const busy = Boolean(state.pending) || models.testing || !models.providerId

  return (
    <section className={styles.page}>
      <header className="page-header">
        <div><h1>Model Routes</h1><p>Pick the models each target serves; aliases stay editable per route</p></div>
        <div className={styles.headerActions}>
          {state.target === 'relay' ? <Button onClick={() => { model.clearError(); setWizard(true) }}><GitBranch size={16} aria-hidden="true" />Build failover chain</Button> : null}
          <Button variant="primary" onClick={() => { model.clearError(); setEditor('new') }}><Plus size={16} />Add route</Button>
        </div>
      </header>

      <div className={styles.toolbar}>
        <Segmented
          label="Route target"
          value={state.target}
          options={[{ id: 'relay' as const, label: 'Local Relay' }, { id: 'tunnel' as const, label: 'Public Tunnel' }]}
          onChange={(target) => void model.load(target)}
        />
        <span>{state.target === 'relay' ? 'Unassigned models use the active provider.' : 'Only enabled aliases in this list appear in /v1/models.'}</span>
      </div>

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
          <Segmented
            label="Catalog filter"
            value={filter}
            options={FILTERS.map((entry) => ({ id: entry.id, label: entry.label }))}
            onChange={setFilter}
          />
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
        <header><div><h2>{state.target === 'relay' ? 'Relay assignments' : 'Published tunnel models'}</h2><p>{state.routes.length} routes{chainCount > 0 ? ` · ${chainCount} chained` : ''}</p></div></header>
        <div className={styles.tableWrap}><table className={styles.table}><thead><tr><th>Public model / alias</th><th>Upstream model</th><th>Provider</th><th>{state.target === 'relay' ? 'Failover order' : 'Context cap'}</th><th>State</th><th>Actions</th></tr></thead><tbody>
          {orderedRoutes.map((route) => {
            const sameModel = orderedRoutes.filter((entry) => entry.publicModel === route.publicModel)
            const chainPosition = sameModel.findIndex((entry) => entry.providerId === route.providerId)
            const chained = state.target === 'relay' && sameModel.length > 1
            return (
              <tr key={`${route.publicModel}:${route.providerId}`} data-chain={chained || undefined}>
                <td>
                  <strong>{route.publicModel}</strong>
                  {route.aliases && route.aliases.length > 0 ? <small className={styles.aliases}> · {route.aliases.join(' · ')}</small> : null}
                  {chained ? <small className={styles.chainNote}>chain of {sameModel.length}</small> : null}
                </td>
                <td>{route.upstreamModel}</td>
                <td>{providerNames.get(route.providerId) ?? route.providerId}</td>
                <td>
                  {state.target === 'relay'
                    ? chained
                      ? <span className={styles.orderControls}>
                          <button type="button" aria-label={`Move ${route.publicModel} on ${providerNames.get(route.providerId) ?? route.providerId} earlier in the chain`} disabled={chainPosition === 0 || Boolean(state.pending)} onClick={() => void model.moveInChain(route, -1)}><ArrowUp size={13} aria-hidden="true" /></button>
                          <span title="A request tries this chain in order and moves on when a provider answers with a verdict no retry could change">{chainPosition + 1} of {sameModel.length}</span>
                          <button type="button" aria-label={`Move ${route.publicModel} on ${providerNames.get(route.providerId) ?? route.providerId} later in the chain`} disabled={chainPosition === sameModel.length - 1 || Boolean(state.pending)} onClick={() => void model.moveInChain(route, 1)}><ArrowDown size={13} aria-hidden="true" /></button>
                        </span>
                      : route.priority ? `first` : '—'
                    : route.contextLimitKiB === 0 ? 'Default' : `${Math.round(route.contextLimitKiB / 1024)} MiB`}
                </td>
                <td>{route.enabled ? 'Enabled' : 'Disabled'}</td>
                <td><div className={styles.actions}><button type="button" aria-label={`Edit ${route.publicModel}`} onClick={() => { model.clearError(); setEditor(route) }}><Pencil /></button><button type="button" aria-label={`Delete ${route.publicModel}`} onClick={() => void model.delete(route.publicModel)}><Trash2 /></button></div></td>
              </tr>
            )
          })}
          {state.phase !== 'loading' && orderedRoutes.length === 0 ? <tr><td colSpan={6} className={styles.empty}>No {state.target === 'relay' ? 'relay assignments' : 'public tunnel models'} yet.</td></tr> : null}
        </tbody></table></div>
      </section>

      {editor ? <RouteEditor target={state.target} route={editor === 'new' ? undefined : editor} providers={providers.catalog.providers.filter((provider) => provider.enabled)} pending={Boolean(state.pending)} operationError={state.error} onClose={() => setEditor(null)} onSave={async route => { if (await model.upsert(route)) setEditor(null) }} /> : null}

      {wizard ? (
        <ChainWizard
          providers={providers.catalog.providers.filter((provider) => provider.enabled)}
          initialPublicModel=""
          pending={Boolean(state.pending)}
          operationError={state.error}
          onClose={() => setWizard(false)}
          discover={modelsModel.catalogOf.bind(modelsModel)}
          onSave={async routes => { await model.upsertMany(routes) }}
        />
      ) : null}
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
        {routed?.tunnel ? <em className={styles.badge} data-target="tunnel" title={`Tunnel alias: ${routed.tunnel}`}>{routed.tunnel === item ? 'Tunnel' : `Tunnel · ${routed.tunnel}`}</em> : null}
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
  const [priority, setPriority] = useState(String(route?.priority ?? 0))
  const [aliases, setAliases] = useState(route?.aliases?.join(' ') ?? '')
  const [enabled, setEnabled] = useState(route?.enabled ?? true)
  const [error, setError] = useState('')
  const dialogRef = useModalFocus<HTMLFormElement>(onClose, pending)
  const submit = (event: FormEvent) => {
    event.preventDefault()
    const context = Number(contextMiB)
    const chainPriority = Number(priority)
    const aliasList = [...new Set(aliases.split(/[\s,]+/u).map((alias) => alias.trim()).filter(Boolean))]
    if (!publicModel.trim() || !upstreamModel.trim() || !providerId || !Number.isFinite(context) || context < 0 || context > 2_048) { setError('Complete both model names, provider and a context limit from 0 to 2048 MiB.'); return }
    if (!Number.isInteger(chainPriority) || chainPriority < 0 || chainPriority > 1000) { setError('Failover order must be a whole number from 0 to 1000.'); return }
    if (aliasList.some((alias) => alias.length > 128 || alias === publicModel.trim())) { setError('Aliases must differ from the requested name and be at most 128 characters.'); return }
    setError('')
    void onSave({ target, publicModel: publicModel.trim(), upstreamModel: upstreamModel.trim(), providerId, contextLimitKiB: Math.round(context * 1024), priority: target === 'relay' ? chainPriority : undefined, aliases: aliasList, enabled })
  }
  return <div className="ui-scrim"><form ref={dialogRef} className={`ui-modal ${styles.routeModal}`} role="dialog" aria-modal="true" aria-label={route ? 'Edit model route' : 'Add model route'} onSubmit={submit}><header><div><h2>{route ? 'Edit route' : 'Add route'}</h2><p>{target === 'tunnel' ? 'Public alias never exposes the upstream model or provider.' : 'Requested model is routed to the selected provider.'}</p></div><button type="button" aria-label="Close" onClick={onClose}><X /></button></header><div className={styles.form}><label><span>{target === 'tunnel' ? 'Public alias' : 'Requested model'}</span><input className={styles.monoInput} value={publicModel} maxLength={128} required disabled={Boolean(route)} data-autofocus onChange={event => setPublicModel(event.currentTarget.value)} /></label><label><span>Upstream model</span><input className={styles.monoInput} value={upstreamModel} maxLength={128} required onChange={event => setUpstreamModel(event.currentTarget.value)} /></label><label><span>Also match</span><input className={styles.monoInput} value={aliases} maxLength={1024} placeholder="claude-opus-5[1m], alias-2" onChange={event => setAliases(event.currentTarget.value)} /><small>Extra requested names that route to this same upstream model, comma or space separated.{target === 'tunnel' ? ' Accepted by the tunnel, but /v1/models lists only the public alias above, which is also the name every answer carries.' : ''}</small></label><label><span>Provider</span><select value={providerId} required onChange={event => setProviderId(event.currentTarget.value)}>{providers.map(provider => <option key={provider.id} value={provider.id}>{provider.name}</option>)}</select></label>{target === 'relay' ? <label><span>Failover order</span><span className={styles.suffixed}><input type="number" min="0" max="1000" step="1" required value={priority} onChange={event => setPriority(event.currentTarget.value)} /><small>lower first</small></span><small>Add the same requested model on another provider with a higher number and a request that gets a final refusal here moves there on its own.</small></label> : null}<label><span>Context limit</span><span className={styles.suffixed}><input type="number" min="0" max="2048" step="0.001" required value={contextMiB} onChange={event => setContextMiB(event.currentTarget.value)} /><small>MiB</small></span></label><label className={styles.check}><input type="checkbox" checked={enabled} onChange={event => setEnabled(event.currentTarget.checked)} />Route enabled</label>{error || operationError ? <p className={styles.formError} role="alert">{error || operationError}</p> : null}</div><footer><Button type="button" onClick={onClose}>Cancel</Button><Button type="submit" variant="primary" disabled={pending}>{pending ? 'Saving…' : 'Save route'}</Button></footer></form></div>
}

/** Builds a failover chain in one pass: one public model, several providers in
 * order, upstream models picked from each provider's discovered catalog. The
 * manual Add-route editor stays for aliases and context caps; this exists so
 * the chain feature is one dialog instead of three careful edits. */
export function ChainWizard({ providers, initialPublicModel, pending, operationError, onClose, discover, onSave }: {
  providers: readonly { id: string; name: string }[]
  initialPublicModel: string
  pending: boolean
  operationError: string
  onClose: () => void
  discover: (providerId: string) => Promise<readonly string[]>
  onSave: (routes: readonly ModelRoute[]) => Promise<unknown>
}) {
  const [publicModel, setPublicModel] = useState(initialPublicModel)
  const [entries, setEntries] = useState<readonly ChainEntry[]>(() => seedChainEntries(providers))
  const [catalogs, setCatalogs] = useState<ReadonlyMap<string, readonly string[]>>(() => new Map())
  // Loading is per provider, not one string: the seed fires a discover for
  // each row back-to-back, and a single slot let the first resolution clear
  // the indicator while other rows were still pending — a waiting row read as
  // a catalog that had come back empty. The set is also the guard that keeps
  // the effect from re-firing a provider that has not answered yet.
  const [loadingProviders, setLoadingProviders] = useState<ReadonlySet<string>>(() => new Set())
  const [error, setError] = useState('')
  const dialogRef = useModalFocus<HTMLFormElement>(onClose, pending)

  useEffect(() => {
    for (const entry of entries) {
      if (catalogs.has(entry.providerId) || entry.providerId === '' || loadingProviders.has(entry.providerId)) continue
      const { providerId } = entry
      setLoadingProviders((current) => new Set(current).add(providerId))
      // The settle path is shared by both outcomes: a rejected discover must
      // leave the row a usable empty catalog, not a "Loading…" that never
      // ends — the production wiring never rejects, but the wizard accepts
      // any discover function and a hung row reads as a broken wizard.
      const settle = () => {
        setLoadingProviders((current) => {
          const settled = new Set(current)
          settled.delete(providerId)
          return settled
        })
      }
      void discover(providerId)
        .then((models) => {
          setCatalogs((current) => new Map(current).set(providerId, models))
          settle()
        })
        .catch(() => {
          setCatalogs((current) => new Map(current).set(providerId, []))
          settle()
        })
    }
    // Catalogs load once per provider; entries changing providers reload theirs.
  }, [catalogs, discover, entries, loadingProviders])

  const setEntry = (index: number, patch: ChainEntryPatch) => {
    setEntries((current) => patchChainEntry(current, index, patch))
  }
  const moveEntry = (index: number, direction: -1 | 1) => {
    setEntries((current) => moveChainEntry(current, index, direction))
  }
  const submit = (event: FormEvent) => {
    event.preventDefault()
    const name = publicModel.trim()
    if (!name) { setError('Name the model your clients will ask for.'); return }
    if (entries.length < 2) { setError('A chain needs at least two providers; a single one is just a route.'); return }
    const seenProviders = new Set<string>()
    for (const entry of entries) {
      if (!entry.providerId || seenProviders.has(entry.providerId)) { setError('Each provider may appear once in the chain.'); return }
      seenProviders.add(entry.providerId)
      if (!entry.upstreamModel.trim()) { setError('Pick the upstream model for every provider.'); return }
    }
    setError('')
    void onSave(entries.map((entry, index) => ({
      target: 'relay' as const,
      publicModel: name,
      upstreamModel: entry.upstreamModel.trim(),
      providerId: entry.providerId,
      contextLimitKiB: 0,
      priority: index,
      aliases: [],
      enabled: true,
    })))
  }

  return (
    <div className="ui-scrim" role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget && !pending) onClose() }}>
      <form ref={dialogRef} className={`ui-modal ${styles.chainModal}`} role="dialog" aria-modal="true" aria-label="Build failover chain" onSubmit={submit}>
        <header>
          <div><h2>Build failover chain</h2><p>One public model, several providers. A request walks them in order; a final refusal moves it to the next.</p></div>
          <button type="button" aria-label="Close" disabled={pending} onClick={onClose}><X /></button>
        </header>
        <div className={styles.form}>
          <label>
            <span>Public model</span>
            <input className={styles.monoInput} value={publicModel} maxLength={128} required data-autofocus placeholder="glm" onChange={(event) => setPublicModel(event.currentTarget.value)} />
            <small>The name your clients ask for. Every provider below serves it under its own upstream name.</small>
          </label>
          <div className={styles.chainRows} role="list" aria-label="Chain providers in order">
            {entries.map((entry, index) => {
              const catalog = catalogs.get(entry.providerId) ?? []
              return (
                <div key={entry.id} className={styles.chainRow} role="listitem">
                  <span className={styles.chainPosition} aria-hidden="true">{index + 1}</span>
                  <label>
                    <span>Provider</span>
                    <select value={entry.providerId} onChange={(event) => setEntry(index, { providerId: event.currentTarget.value, upstreamModel: '' })}>
                      {providers.map((provider) => <option key={provider.id} value={provider.id}>{provider.name}</option>)}
                    </select>
                  </label>
                  <label>
                    <span>Upstream model</span>
                    {catalog.length > 0 ? (
                      <select value={entry.upstreamModel} onChange={(event) => setEntry(index, { upstreamModel: event.currentTarget.value })}>
                        <option value="">{loadingProviders.has(entry.providerId) ? 'Loading…' : 'Pick a model'}</option>
                        {catalog.map((model) => <option key={model} value={model}>{model}</option>)}
                      </select>
                    ) : (
                      <input value={entry.upstreamModel} maxLength={128} placeholder={loadingProviders.has(entry.providerId) ? 'Loading…' : 'Model name on this provider'} onChange={(event) => setEntry(index, { upstreamModel: event.currentTarget.value })} />
                    )}
                  </label>
                  <div className={styles.chainRowActions}>
                    <button type="button" aria-label="Move earlier in the chain" disabled={index === 0} onClick={() => moveEntry(index, -1)}><ArrowUp size={14} /></button>
                    <button type="button" aria-label="Move later in the chain" disabled={index === entries.length - 1} onClick={() => moveEntry(index, 1)}><ArrowDown size={14} /></button>
                    <button type="button" aria-label="Remove from the chain" disabled={entries.length <= 2} onClick={() => setEntries((current) => removeChainEntry(current, index))}><Trash2 size={14} /></button>
                  </div>
                </div>
              )
            })}
          </div>
          <Button type="button" disabled={entries.length >= providers.length} onClick={() => setEntries((current) => addChainEntry(current, providers))}><Plus size={14} />Add provider</Button>
          {error || operationError ? <p className={styles.formError} role="alert">{error || operationError}</p> : null}
        </div>
        <footer>
          <Button type="button" disabled={pending} onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={pending || !publicModel.trim()}>{pending ? 'Saving…' : `Save chain of ${entries.length}`}</Button>
        </footer>
      </form>
    </div>
  )
}
