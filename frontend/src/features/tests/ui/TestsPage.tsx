import { useEffect, useMemo, useState, useSyncExternalStore } from 'react'
import { FlaskConical, Play, RefreshCw, Square } from 'lucide-react'
import { useAppServices } from '../../../app/services'
import { useProviders } from '../../providers/ui/useProviders'
import { formatClock, formatDuration } from '../../../shared/format/metrics'
import { Button } from '../../../shared/ui/Button'
import { EmptyState, Panel, Pill, Segmented } from '../../../shared/ui/chrome'
import { StatusDot } from '../../../shared/ui/StatusDot'
import type { ModelTestResult } from '../../models/domain/model'
import { testResultKey, type TestTarget } from '../application/tests-model'
import styles from './TestsPage.module.css'

type SortKey = 'model' | 'ttft' | 'total' | 'rate'

/** Text probes cannot measure an image generator — it answers 400 on the
 * chat endpoints, which is not a failure worth a red row. */
const IMAGE_MODEL = /image|dall-e|flux|seedream|imagen/i

function tokensPerSecond(result: ModelTestResult): number {
  if (!result.outputTokens || !result.latencyMs) return 0
  // Decode rate: the generation proper starts at the first token.
  const decodeMs = result.latencyMs - (result.ttftMs ?? 0)
  if (decodeMs <= 0) return 0
  return (result.outputTokens / decodeMs) * 1000
}

export default function TestsPage() {
  const services = useAppServices()
  const tests = services.tests
  const state = useSyncExternalStore(tests.subscribe, tests.snapshot)
  const { state: providers } = useProviders()
  const [scope, setScope] = useState('all')
  const [sort, setSort] = useState<SortKey>('model')
  const [ascending, setAscending] = useState(true)

  const enabledProviders = useMemo(
    () => providers.catalog.providers.filter((provider) => provider.enabled),
    [providers.catalog.providers],
  )
  // A fresh array every render would re-fire the catalog effect; the scope
  // changes the list, nothing else does.
  const scopedProviders = useMemo(
    () => scope === 'all' ? enabledProviders : enabledProviders.filter((provider) => provider.id === scope),
    [enabledProviders, scope],
  )

  // Catalogs arrive lazily: the page asks for what it shows, and the model
  // answers from its cache on the second visit.
  useEffect(() => {
    for (const provider of scopedProviders) void tests.ensureCatalog(provider.id)
  }, [tests, scopedProviders])

  const rows = useMemo(() => {
    const list: { providerId: string; providerName: string; model: string; result: ModelTestResult | null }[] = []
    for (const provider of scopedProviders) {
      const catalog = state.catalogs[provider.id]
      if (!Array.isArray(catalog)) continue
      for (const model of catalog) {
        list.push({ providerId: provider.id, providerName: provider.name, model, result: state.results[testResultKey(provider.id, model)] ?? null })
      }
    }
    const direction = ascending ? 1 : -1
    // One sink for "no data" on every numeric column: absent and zero both
    // mean unmeasured, and unmeasured always sorts last ascending.
    const numeric = (result: ModelTestResult | null, read: (result: ModelTestResult) => number): number => {
      if (!result) return Number.MAX_SAFE_INTEGER
      const value = read(result)
      return value > 0 ? value : Number.MAX_SAFE_INTEGER
    }
    list.sort((left, right) => {
      switch (sort) {
        case 'ttft': return (numeric(left.result, (r) => r.ttftMs ?? 0) - numeric(right.result, (r) => r.ttftMs ?? 0)) * direction
        case 'total': return (numeric(left.result, (r) => r.latencyMs) - numeric(right.result, (r) => r.latencyMs)) * direction
        case 'rate': return (numeric(left.result, tokensPerSecond) - numeric(right.result, tokensPerSecond)) * direction
        default: return (left.model.localeCompare(right.model) || left.providerName.localeCompare(right.providerName)) * direction
      }
    })
    return list
  }, [scopedProviders, state.catalogs, state.results, sort, ascending])

  // A run starts when every catalog in scope answered: silently skipping a
  // provider whose catalog errored would read as measured-and-fine.
  const catalogsReady = scopedProviders.every((provider) => Array.isArray(state.catalogs[provider.id]))
  const erroredCatalogs = scopedProviders.filter((provider) => state.catalogs[provider.id] === 'error')
  // A scope of only image generators has nothing a text probe can measure.
  const testableRows = rows.filter((row) => !IMAGE_MODEL.test(row.model)).length

  const runScope = () => {
    const targets: TestTarget[] = []
    for (const provider of scopedProviders) {
      const catalog = state.catalogs[provider.id]
      const testable = Array.isArray(catalog) ? catalog.filter((model) => !IMAGE_MODEL.test(model)) : []
      if (testable.length > 0) targets.push({ providerId: provider.id, providerName: provider.name, models: testable })
    }
    void tests.run(targets)
  }

  const sortable = (key: SortKey, label: string, numeric = false) => (
    <th aria-sort={sort === key ? (ascending ? 'ascending' : 'descending') : 'none'} className={numeric ? styles.num : undefined}>
      <button type="button" className={styles.sort} title={`Sort by ${label}`} onClick={() => { if (sort === key) setAscending(!ascending); else { setSort(key); setAscending(true) } }}>
        {label}<span className={styles.sortArrow} aria-hidden="true">{sort === key ? (ascending ? '↑' : '↓') : '↕'}</span>
      </button>
    </th>
  )

  return (
    <section className={styles.page}>
      <header className="page-header">
        <div>
          <h1>Tests</h1>
          <p>Real streaming probes against every model in scope — first token, total, decode rate. A probe never queues ahead of your traffic.</p>
        </div>
        <div className={styles.headerActions}>
          {state.lastRunAt && !state.running ? <span className={styles.lastRun}>Last run {formatClock(state.lastRunAt)}</span> : null}
          {state.running
            ? <Button variant="secondary" onClick={() => tests.cancel()}><Square size={14} />Stop</Button>
            : <Button variant="primary" disabled={testableRows === 0 || !catalogsReady} onClick={runScope}><Play size={14} />Run {scope === 'all' ? 'all' : 'shown'}</Button>}
        </div>
      </header>
      {state.running ? (
        <div className={styles.runProgress} role="status">
          <div className={styles.runTrack}><div className={styles.runFill} style={{ width: `${state.runTotal > 0 ? Math.round(100 * state.runDone / state.runTotal) : 0}%` }} /></div>
          <span>{state.runDone} of {state.runTotal} measured{state.runFailed > 0 ? ` · ${state.runFailed} failed` : ''}</span>
        </div>
      ) : null}
      {state.error ? <div className={styles.error} role="alert">{state.error}</div> : null}
      {erroredCatalogs.length > 0 ? (
        <div className={styles.error} role="alert">
          No model catalog from {erroredCatalogs.map((provider) => provider.name).join(', ')} — check the provider and its key, then Refresh catalog.
        </div>
      ) : null}

      <div className="page-body">
        <Panel
          title="Providers"
          subtitle={`${scopedProviders.length} in scope · ${rows.length} models`}
          actions={
            <div className={styles.scopeRow}>
              <Segmented
                label="Provider scope"
                value={scope}
                onChange={setScope}
                options={[
                  { id: 'all', label: 'All providers' },
                  ...enabledProviders.map((provider) => ({ id: provider.id, label: provider.name })),
                ]}
              />
              {scope !== 'all' ? (
                <Button variant="ghost" disabled={state.catalogs[scope] === 'loading' || state.running} onClick={() => void tests.refreshCatalog(scope)}>
                  <RefreshCw size={14} />Refresh catalog
                </Button>
              ) : null}
            </div>
          }
        >
          {rows.length === 0 ? (
            <EmptyState
              icon={FlaskConical}
              title={scopedProviders.some((provider) => state.catalogs[provider.id] === 'loading') ? 'Reading model catalogs…' : 'No models in scope'}
              hint={scopedProviders.length === 0 ? 'Enable a provider first — the relay tests what is configured.' : 'A provider without a readable catalog shows nothing here; check its key on API Keys.'}
            />
          ) : (
            <div className={styles.tableWrap}>
              <table className={styles.table}>
                <thead>
                  <tr>
                    <th>State</th>
                    {sortable('model', 'Model')}
                    <th>Provider</th>
                    {sortable('ttft', 'First token', true)}
                    {sortable('total', 'Total', true)}
                    {sortable('rate', 'Tok/s', true)}
                    <th><span className="sr-only">Actions</span></th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((row) => <TestRow key={testResultKey(row.providerId, row.model)} row={row} running={state.running} onRun={() => void tests.run([{ providerId: row.providerId, providerName: row.providerName, models: [row.model] }])} />)}
                </tbody>
              </table>
            </div>
          )}
        </Panel>
      </div>
    </section>
  )
}

function TestRow({ row, running, onRun }: { row: { providerId: string; providerName: string; model: string; result: ModelTestResult | null }; running: boolean; onRun: () => void }) {
  const result = row.result
  const state = result?.state
  const imageModel = IMAGE_MODEL.test(row.model)
  if (imageModel) {
    // The per-row Run stays: the name heuristic can be wrong, and a model the
    // filter mislabels must remain testable by hand.
    return (
      <tr>
        <td><Pill>Image model</Pill></td>
        <td className={styles.modelCell} title={row.model}>{row.model}</td>
        <td>{row.providerName}</td>
        <td className={styles.num} colSpan={3}><span className={styles.imageNote}>text probe not applicable</span></td>
        <td className={styles.rowActions}><button type="button" className={styles.rowButton} disabled={running} onClick={onRun}><Play size={12} aria-hidden="true" />Run</button></td>
      </tr>
    )
  }
  return (
    <tr data-testing={state === 'testing' || undefined}>
      <td>
        {state === 'testing' ? <Pill tone="info"><StatusDot state="active" />Testing</Pill>
          : state === 'available' ? <Pill tone="success"><StatusDot state="healthy" />OK</Pill>
          : state === 'timeout' ? <Pill tone="warning">Timeout</Pill>
          : state === 'unavailable' && result ? <Pill tone={result.errorCode === 'pool_busy' ? 'warning' : 'danger'}>{result.errorCode === 'result_missing' ? 'No answer' : result.errorCode === 'interrupted' ? 'Stopped' : result.errorCode === 'provider_failed' ? 'Unreachable' : result.errorCode === 'pool_busy' ? 'Pool busy' : `HTTP ${result.status || '—'}`}</Pill>
          : <Pill>Untested</Pill>}
      </td>
      <td className={styles.modelCell} title={row.model}>{row.model}</td>
      <td>{row.providerName}</td>
      <td className={styles.num}>{result?.ttftMs ? formatDuration(result.ttftMs) : '—'}</td>
      <td className={styles.num}>{result?.latencyMs ? formatDuration(result.latencyMs) : '—'}</td>
      <td className={styles.num}>{result && tokensPerSecond(result) > 0 ? tokensPerSecond(result).toFixed(1) : '—'}</td>
      <td className={styles.num}>
        <button type="button" className={styles.rowRun} disabled={running} aria-label={`Test ${row.model} on ${row.providerName}`} onClick={onRun}>
          <Play size={13} aria-hidden="true" />
        </button>
      </td>
    </tr>
  )
}
