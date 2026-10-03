import { useEffect, useMemo, useState, useSyncExternalStore } from 'react'
import { Coins, RefreshCw } from 'lucide-react'
import type { InsightsPeriod, InsightsProvider, InsightsModel, HistoryRequest } from '../domain/insights'
import { useInsights } from './useInsights'
import { useAppServices } from '../../../app/services'
import { PriceEditor } from './PriceEditor'
import { DailyChart } from './DailyChart'
import { formatClock, formatCost, formatDecimal, formatDuration, formatInteger } from '../../../shared/format/metrics'
import { Metric, MetricStrip, Panel, Segmented } from '../../../shared/ui/chrome'
import { StatusDot } from '../../../shared/ui/StatusDot'
import chrome from '../../../shared/ui/chrome.module.css'
import styles from './InsightsPage.module.css'

const periods: readonly InsightsPeriod[] = ['24h', '48h', '72h', 'all']

const skeletonMetrics = ['Requests', 'Success rate', 'Estimated cost', 'p95 latency', 'Tokens']

const stateLabels: Record<HistoryRequest['state'], string> = {
  active: 'Streaming',
  retrying: 'Retrying',
  completed: 'Complete',
  failed: 'Error',
  cancelled: 'Cancelled',
}

// Error previews stay short: a persisted diagnostic can run to thousands of
// runes, and the full text lives behind the row's tooltip.
const errorPreviewLimit = 180

/** The quiet re-read cadence: numbers stay honest without a visible reload. */
const AUTO_REFRESH_MS = 60_000

type ProviderSortKey = 'name' | 'requests' | 'success' | 'latency' | 'retries' | 'tokens' | 'cost'
type ModelSortKey = 'model' | 'requests' | 'success' | 'tokens' | 'tps' | 'cost'

export default function InsightsPage() {
  const { model, state } = useInsights()
  // The providers table also answers "how much of it is published": the relay
  // route count comes from the routes model, loaded once and only read.
  const { routes } = useAppServices()
  const routesState = useSyncExternalStore(routes.subscribe, routes.snapshot)
  useEffect(() => { if (routesState.phase === 'idle') void routes.load('relay') }, [routes, routesState.phase])
  const routeCounts = useMemo(() => {
    const counts = new Map<string, number>()
    for (const route of routesState.published.relay) {
      if (route.enabled) counts.set(route.providerId, (counts.get(route.providerId) ?? 0) + 1)
    }
    return counts
  }, [routesState.published.relay])
  // A model row's price shortcut opens the editor with that model prefilled.
  const [presetModel, setPresetModel] = useState<string | null>(null)
  useEffect(() => { if (state.phase === 'idle') void model.load('24h') }, [model, state.phase])
  // refresh() swaps the report in place; the phase never leaves ready, so the
  // period switch and the refresh button stay usable through every cycle.
  useEffect(() => {
    const timer = window.setInterval(() => void model.refresh(), AUTO_REFRESH_MS)
    return () => window.clearInterval(timer)
  }, [model])
  const report = state.report
  const loading = state.phase === 'loading' || state.phase === 'idle'
  const showSkeleton = !report
  const overview = report?.overview
  const pricedShare = overview && overview.volume.requests > 0
    ? Math.round(100 * overview.pricedRequests / Math.max(overview.volume.completed, 1))
    : 0
  const [providerSort, setProviderSort] = useSort<ProviderSortKey>('tokens')
  const [modelSort, setModelSort] = useSort<ModelSortKey>('tokens')
  const providers = useMemo(
    () => sortProviders(report?.providers ?? [], providerSort),
    [report?.providers, providerSort],
  )
  const models = useMemo(
    () => sortModels(report?.models ?? [], modelSort),
    [report?.models, modelSort],
  )
  const failedRequests = useMemo(
    () => state.recent.filter((request) => request.errorDetail || request.errorCode),
    [state.recent],
  )
  return (
    <section className={styles.page}>
      <header className="page-header">
        <div><h1>Insights</h1><p>Where the tokens, the money and the failures actually go</p></div>
        <div className={styles.actions}>
          <Segmented<InsightsPeriod>
            label="Insights period"
            value={state.period}
            disabled={loading}
            options={periods.map((period) => ({ id: period, label: period === 'all' ? 'All' : period }))}
            onChange={(period) => void model.load(period)}
          />
          <PriceEditor
            prices={state.prices}
            currency={state.pricesCurrency}
            phase={state.pricesPhase}
            knownModels={(report?.models ?? []).map((entry) => entry.model)}
            presetModel={presetModel}
            onPresetHandled={() => setPresetModel(null)}
            onSave={(draft) => model.savePrice(draft)}
            onRemove={(entry) => model.removePrice(entry)}
            onSetCurrency={(currency) => model.setCurrency(currency)}
          />
          <button type="button" className={styles.refresh} disabled={loading} onClick={() => void model.load(state.period)}>
            <RefreshCw size={15} aria-hidden="true" />{loading ? 'Refreshing…' : 'Refresh'}
          </button>
        </div>
      </header>
      {state.error ? <div className={styles.error} role="alert">{state.error}</div> : null}
      {showSkeleton && loading ? <p className={styles.loading} role="status">Loading insights…</p> : null}
      <div className="page-body">
        <div aria-busy={loading}>
          {showSkeleton
            ? (
              <div className={chrome.metricStrip} aria-hidden="true">
                {skeletonMetrics.map((label) => (
                  <div key={label} className={chrome.metric}><span className={chrome.metricLabel}>{label}</span><strong className={`${chrome.metricValue} ${styles.skeleton}`} /></div>
                ))}
              </div>
            )
            : (
              <MetricStrip>
                <Metric label="Requests" value={formatInteger(overview?.volume.requests)} detail={`${formatInteger(overview?.volume.completed)} completed · ${formatInteger(overview?.volume.failed)} failed`} />
                <Metric label="Success rate" value={overview ? `${Math.round(overview.successRate * 100)}%` : '—'} detail={`${formatInteger(overview?.volume.retries)} retries${overview?.topErrorCode ? ` · ${overview.topErrorCode}` : ''}`} tone={overview && overview.successRate < 0.9 ? 'warning' : undefined} />
                <Metric
                  label="Estimated cost"
                  value={overview ? (overview.volume.isPriced ? formatCost(overview.volume.cost, state.pricesCurrency) : formatPartialCost(overview.volume.cost, state.pricesCurrency)) : '—'}
                  detail={overview?.volume.isPriced ? 'priced estimate' : `${pricedShare}% of requests priced`}
                />
                <Metric label="p95 latency" value={formatDuration(overview?.p95Ms ?? 0)} detail={`p50 ${formatDuration(overview?.p50Ms ?? 0)}`} />
                <Metric label="Tokens" value={formatInteger(overview?.volume.totalTokens)} detail={`${formatInteger(overview?.volume.cachedTokens)} cached · ${formatInteger(overview?.volume.reasoningTokens)} reasoning`} />
              </MetricStrip>
            )}
        </div>
        {report && report.daily.length > 1 ? (
          <Panel title="Daily usage" subtitle={report.generatedAt ? `Updated ${formatClock(report.generatedAt)} · auto-refresh every minute` : 'Requests and estimated cost per day'}>
            <DailyChart daily={report.daily} currency={state.pricesCurrency} />
          </Panel>
        ) : null}
        {report && report.providers.length > 0 ? (
          <Panel title="Providers" subtitle="Reliability and speed per provider — click a column to sort">
            <div className={chrome.tableWrap}><table className={`${chrome.dataTable} ${styles.wideProviders}`} aria-label="Provider breakdown"><thead><tr>
              <SortHeader<ProviderSortKey> label="Provider" sortKey="name" active={providerSort} onSort={setProviderSort} />
              <th className={styles.num} scope="col">Relay routes</th>
              <SortHeader<ProviderSortKey> label="Requests" sortKey="requests" active={providerSort} onSort={setProviderSort} numeric />
              <SortHeader<ProviderSortKey> label="Success" sortKey="success" active={providerSort} onSort={setProviderSort} numeric />
              <SortHeader<ProviderSortKey> label="p50 / p95" sortKey="latency" active={providerSort} onSort={setProviderSort} numeric />
              <SortHeader<ProviderSortKey> label="Retries" sortKey="retries" active={providerSort} onSort={setProviderSort} numeric />
              <SortHeader<ProviderSortKey> label="Tokens" sortKey="tokens" active={providerSort} onSort={setProviderSort} numeric />
              <SortHeader<ProviderSortKey> label="Est. cost" sortKey="cost" active={providerSort} onSort={setProviderSort} numeric />
            </tr></thead><tbody>
              {providers.map((provider) => (
                <tr key={provider.id}>
                  <td title={provider.id}>{provider.name || provider.id}</td>
                  <td className={styles.num}>{routeCounts.get(provider.id) ?? 0}</td>
                  <td className={styles.num}>{formatInteger(provider.volume.requests)}</td>
                  <td className={styles.num}>{formatRate(provider.volume.completed, provider.volume.requests)}</td>
                  <td className={styles.num}>{formatDuration(provider.p50Ms)} / {formatDuration(provider.p95Ms)}</td>
                  <td className={styles.num}>{formatInteger(provider.volume.retries)}</td>
                  <td className={styles.num}>{formatInteger(provider.volume.totalTokens)}</td>
                  <td className={styles.num}>{provider.volume.isPriced ? formatCost(provider.volume.cost, state.pricesCurrency) : formatPartialCost(provider.volume.cost, state.pricesCurrency)}</td>
                </tr>
              ))}
            </tbody></table></div>
          </Panel>
        ) : null}
        {report && report.models.length > 0 ? (
          <Panel title="Models" subtitle="What the tokens were spent on — click a column to sort">
            <div className={chrome.tableWrap}><table className={`${chrome.dataTable} ${styles.wideModels}`} aria-label="Model breakdown"><thead><tr>
              <SortHeader<ModelSortKey> label="Model" sortKey="model" active={modelSort} onSort={setModelSort} />
              <SortHeader<ModelSortKey> label="Requests" sortKey="requests" active={modelSort} onSort={setModelSort} numeric />
              <SortHeader<ModelSortKey> label="Success" sortKey="success" active={modelSort} onSort={setModelSort} numeric />
              <SortHeader<ModelSortKey> label="Tokens" sortKey="tokens" active={modelSort} onSort={setModelSort} numeric />
              <SortHeader<ModelSortKey> label="Tok/s" sortKey="tps" active={modelSort} onSort={setModelSort} numeric />
              <SortHeader<ModelSortKey> label="Est. cost" sortKey="cost" active={modelSort} onSort={setModelSort} numeric />
              <th scope="col"><span className={styles.srOnly}>Price</span></th>
            </tr></thead><tbody>
              {models.map((entry) => {
                const priced = state.prices.some((price) => price.model === entry.model)
                return (
                  <tr key={entry.model}>
                    <td title={entry.model}>{entry.model}</td>
                    <td className={styles.num}>{formatInteger(entry.volume.requests)}</td>
                    <td className={styles.num}>{formatRate(entry.volume.completed, entry.volume.requests)}</td>
                    <td className={styles.num}>{formatInteger(entry.volume.totalTokens)}</td>
                    <td className={styles.num}>{entry.tokensPerSecond > 0 ? formatDecimal(entry.tokensPerSecond, 0) : '—'}</td>
                    <td className={styles.num}>{entry.volume.isPriced ? formatCost(entry.volume.cost, state.pricesCurrency) : formatPartialCost(entry.volume.cost, state.pricesCurrency)}</td>
                    <td className={styles.priceCell}>
                      <button type="button" className={styles.priceAction} data-priced={priced || undefined} onClick={() => setPresetModel(entry.model)}>
                        <Coins size={12} aria-hidden="true" />{priced ? 'Edit price' : 'Set price'}
                      </button>
                    </td>
                  </tr>
                )
              })}
            </tbody></table></div>
          </Panel>
        ) : null}
        {report && report.errors.length > 0 ? (
          <Panel title="Failures" subtitle="The relay's own error codes, ranked">
            <div className={styles.errorBars}>
              {report.errors.map((error) => {
                const share = report.overview.volume.requests > 0 ? 100 * error.requests / report.overview.volume.requests : 0
                return (
                  <div key={error.errorCode} className={styles.errorBar}>
                    <span className={styles.errorCode} title={error.errorCode}>{error.errorCode}</span>
                    <span className={styles.errorTrack} aria-hidden="true"><span className={styles.errorFill} style={{ width: `${Math.max(share, share > 0 ? 2 : 0)}%` }} /></span>
                    <span className={styles.errorCount}>{formatInteger(error.requests)}</span>
                  </div>
                )
              })}
            </div>
          </Panel>
        ) : null}
        {report && report.unpricedModels.length > 0 ? (
          <Panel title={<><Coins size={14} aria-hidden="true" /> No price set</>} subtitle="The estimate does not cover these models">
            <p className={styles.unpriced}>{report.unpricedModels.join(' · ')}</p>
          </Panel>
        ) : null}
        {state.phase !== 'idle' && state.phase !== 'error' ? (
          <Panel title="Recent requests" subtitle={state.recent.length > 0 ? `${state.recentAvailable > state.recent.length ? `Newest ${state.recent.length} of ${state.recentAvailable} rows` : `${state.recent.length} rows`} in ${state.period === 'all' ? 'history' : `the last ${state.period}`}` : 'No persisted requests in this period'}>
            <div className={chrome.tableWrap}><table className={`${chrome.dataTable} ${styles.wideRecent}`} aria-label="Recent persisted requests"><thead><tr>
              <th scope="col">State</th><th scope="col">Model</th><th scope="col">Provider</th><th scope="col" className={styles.num}>HTTP</th><th scope="col" className={styles.num}>Latency</th><th scope="col" className={styles.num}>Processed</th><th scope="col" className={styles.num}>Cached</th><th scope="col" className={styles.num}>Time</th>
            </tr></thead><tbody>
              {loading && state.recent.length === 0
                ? [0, 1, 2].map((row) => <tr key={row} className={styles.skeletonRow} aria-hidden="true"><td colSpan={8}><span className={styles.skeleton} /></td></tr>)
                : state.recent.map((request) => (
                  <tr key={request.id} data-failed={request.state === 'failed' || undefined}>
                    <td><span className={styles.state}><StatusDot state={request.state} />{stateLabels[request.state]}</span></td>
                    <td title={request.model}>{request.model || '—'}</td>
                    <td>{request.providerId || '—'}</td>
                    <td className={styles.num}>{request.status || '—'}</td>
                    <td className={styles.num}>{formatDuration(request.latencyMs)}</td>
                    <td className={styles.num}>{formatInteger(request.totalTokens)}</td>
                    <td className={styles.num}>{formatInteger(request.cachedTokens)}</td>
                    <td className={styles.num}>{formatClock(request.updatedAt)}</td>
                  </tr>
                ))}
              {!loading && state.recent.length === 0 ? <tr><td colSpan={8} className={styles.empty}>No persisted requests in this period.</td></tr> : null}
            </tbody></table></div>
          </Panel>
        ) : null}
        {failedRequests.length > 0 ? (
          <Panel title="Request errors" subtitle="Full diagnostics from the persisted rows">
            <div className={styles.errorList}>
              {failedRequests.map((request) => {
                const full = request.errorDetail || request.errorCode || ''
                return (
                  <article key={request.id} className={styles.errorItem}>
                    <strong title={full} tabIndex={0} aria-label={full}>{previewError(full)}</strong>
                    <small>{request.model || 'Unknown model'} — {formatClock(request.updatedAt)}</small>
                  </article>
                )
              })}
            </div>
          </Panel>
        ) : null}
      </div>
    </section>
  )
}

function useSort<Key extends string>(initial: Key): [{ key: Key; descending: boolean }, (key: Key) => void] {
  const [sort, setSort] = useState({ key: initial, descending: true })
  const choose = (key: Key) => setSort((current) => {
    if (current.key === key) return { key, descending: !current.descending }
    // Numbers start biggest-first, names start alphabetical: the direction
    // a person means when they reach for a column.
    const textFirst = key === 'name' || key === 'model'
    return { key, descending: !textFirst }
  })
  return [sort, choose]
}

function SortHeader<Key extends string>({ label, sortKey, active, onSort, numeric = false }: {
  label: string
  sortKey: Key
  active: { key: Key; descending: boolean }
  onSort: (key: Key) => void
  numeric?: boolean
}) {
  const isActive = active.key === sortKey
  return (
    <th
      scope="col"
      aria-sort={isActive ? (active.descending ? 'descending' : 'ascending') : 'none'}
      className={numeric ? styles.num : undefined}
    >
      <button type="button" className={styles.sortButton} data-active={isActive || undefined} onClick={() => onSort(sortKey)}>
        {numeric ? <span aria-hidden="true" className={styles.sortArrow} data-visible={isActive || undefined}>{isActive && !active.descending ? '↑' : '↓'}</span> : null}
        {label}
        {numeric ? null : <span aria-hidden="true" className={styles.sortArrow} data-visible={isActive || undefined}>{isActive && !active.descending ? '↑' : '↓'}</span>}
      </button>
    </th>
  )
}

function sortProviders(rows: readonly InsightsProvider[], sort: { key: ProviderSortKey; descending: boolean }): InsightsProvider[] {
  const sorted = [...rows]
  sorted.sort((left, right) => {
    let comparison = 0
    switch (sort.key) {
      case 'name':
        comparison = (left.name || left.id).localeCompare(right.name || right.id)
        break
      case 'requests':
        comparison = left.volume.requests - right.volume.requests
        break
      case 'success':
        comparison = successRate(left.volume) - successRate(right.volume)
        break
      case 'latency':
        comparison = left.p95Ms - right.p95Ms
        break
      case 'retries':
        comparison = left.volume.retries - right.volume.retries
        break
      case 'cost':
        comparison = left.volume.cost - right.volume.cost
        break
      default:
        comparison = left.volume.totalTokens - right.volume.totalTokens
    }
    return sort.descending ? -comparison : comparison
  })
  return sorted
}

function sortModels(rows: readonly InsightsModel[], sort: { key: ModelSortKey; descending: boolean }): InsightsModel[] {
  const sorted = [...rows]
  sorted.sort((left, right) => {
    let comparison = 0
    switch (sort.key) {
      case 'model':
        comparison = left.model.localeCompare(right.model)
        break
      case 'requests':
        comparison = left.volume.requests - right.volume.requests
        break
      case 'success':
        comparison = successRate(left.volume) - successRate(right.volume)
        break
      case 'tps':
        comparison = left.tokensPerSecond - right.tokensPerSecond
        break
      case 'cost':
        comparison = left.volume.cost - right.volume.cost
        break
      default:
        comparison = left.volume.totalTokens - right.volume.totalTokens
    }
    return sort.descending ? -comparison : comparison
  })
  return sorted
}

function formatRate(part: number, total: number): string {
  if (total <= 0) return '—'
  return `${Math.round(100 * part / total)}%`
}

function successRate(volume: { requests: number; completed: number }): number {
  if (volume.requests <= 0) return 0
  return volume.completed / volume.requests
}

function formatPartialCost(cost: number, currency: string): string {
  return cost > 0 ? `≥ ${formatCost(cost, currency)}` : '—'
}

function previewError(value: string): string {
  return value.length > errorPreviewLimit ? `${value.slice(0, errorPreviewLimit)}…` : value
}
