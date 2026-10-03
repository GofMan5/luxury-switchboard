import { useEffect, useMemo, useState, useSyncExternalStore } from 'react'
import { FlaskConical, Play, RefreshCw, Square } from 'lucide-react'
import { useAppServices } from '../../../app/services'
import { useProviders } from '../../providers/ui/useProviders'
import { formatDecimal, formatDuration } from '../../../shared/format/metrics'
import { Button } from '../../../shared/ui/Button'
import { EmptyState, Panel, Pill, Segmented } from '../../../shared/ui/chrome'
import { StatusDot } from '../../../shared/ui/StatusDot'
import type { ModelTestResult } from '../../models/domain/model'
import { testResultKey, type TestTarget } from '../application/tests-model'
import styles from './TestsPage.module.css'

type SortKey = 'model' | 'ttft' | 'total' | 'rate'

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
  const scopedProviders = scope === 'all' ? enabledProviders : enabledProviders.filter((provider) => provider.id === scope)

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
    list.sort((left, right) => {
      switch (sort) {
        case 'ttft': return ((left.result?.ttftMs ?? Number.MAX_SAFE_INTEGER) - (right.result?.ttftMs ?? Number.MAX_SAFE_INTEGER)) * direction
        case 'total': return ((left.result?.latencyMs || Number.MAX_SAFE_INTEGER) - (right.result?.latencyMs || Number.MAX_SAFE_INTEGER)) * direction
        case 'rate': return ((left.result ? tokensPerSecond(left.result) : -1) - (right.result ? tokensPerSecond(right.result) : -1)) * direction
        default: return (left.model.localeCompare(right.model) || left.providerName.localeCompare(right.providerName)) * direction
      }
    })
    return list
  }, [scopedProviders, state.catalogs, state.results, sort, ascending])

  const runScope = () => {
    const targets: TestTarget[] = []
    for (const provider of scopedProviders) {
      const catalog = state.catalogs[provider.id]
      if (Array.isArray(catalog)) targets.push({ providerId: provider.id, providerName: provider.name, models: catalog })
    }
    void tests.run(targets)
  }

  const sortable = (key: SortKey, label: string) => (
    <th aria-sort={sort === key ? (ascending ? 'ascending' : 'descending') : 'none'}>
      <button type="button" className={styles.sort} onClick={() => { if (sort === key) setAscending(!ascending); else { setSort(key); setAscending(true) } }}>
        {label}<span aria-hidden="true">{sort === key ? (ascending ? '↑' : '↓') : ''}</span>
      </button>
    </th>
  )

  return (
    <section className={styles.page}>
      <header className="page-header">
        <div>
          <h1>Tests</h1>
          <p>Real streaming probes: first token, total time, decode rate</p>
        </div>
        {state.running
          ? <Button variant="secondary" onClick={() => tests.cancel()}><Square size={14} />Stop</Button>
          : <Button variant="primary" disabled={rows.length === 0} onClick={runScope}><Play size={14} />Run {scope === 'all' ? 'all' : 'shown'}</Button>}
      </header>

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
                    {sortable('ttft', 'First token')}
                    {sortable('total', 'Total')}
                    {sortable('rate', 'Tok/s')}
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
  return (
    <tr data-testing={state === 'testing' || undefined}>
      <td>
        {state === 'testing' ? <span className={styles.state}><StatusDot state="active" />Testing</span>
          : state === 'available' ? <span className={styles.state}><StatusDot state="healthy" />OK</span>
          : state === 'timeout' ? <Pill tone="warning">Timeout</Pill>
          : state === 'unavailable' && result ? <Pill tone="danger">{result.errorCode === 'result_missing' ? 'No answer' : result.errorCode === 'interrupted' ? 'Stopped' : `HTTP ${result.status || '—'}`}</Pill>
          : <Pill>Untested</Pill>}
      </td>
      <td className={styles.modelCell} title={row.model}>{row.model}</td>
      <td>{row.providerName}</td>
      <td className={styles.num}>{result?.ttftMs ? formatDuration(result.ttftMs) : '—'}</td>
      <td className={styles.num}>{result?.latencyMs ? formatDuration(result.latencyMs) : '—'}</td>
      <td className={styles.num}>{result && tokensPerSecond(result) > 0 ? formatDecimal(tokensPerSecond(result)) : '—'}</td>
      <td className={styles.num}>
        <button type="button" className={styles.rowRun} disabled={running} aria-label={`Test ${row.model} on ${row.providerName}`} onClick={onRun}>
          <Play size={13} aria-hidden="true" />
        </button>
      </td>
    </tr>
  )
}
