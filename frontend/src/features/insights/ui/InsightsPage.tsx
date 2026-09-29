import { useEffect, useMemo, useState } from 'react'
import { Coins, RefreshCw } from 'lucide-react'
import type { InsightsPeriod, InsightsProvider, InsightsModel, HistoryRequest } from '../domain/insights'
import { useInsights } from './useInsights'
import { PriceEditor } from './PriceEditor'
import { DailyChart } from './DailyChart'
import { formatClock } from '../../../shared/format/metrics'
import { StatusDot } from '../../../shared/ui/StatusDot'
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

type ProviderSortKey = 'name' | 'requests' | 'success' | 'latency' | 'retries' | 'tokens' | 'cost'
type ModelSortKey = 'model' | 'requests' | 'success' | 'tokens' | 'tps' | 'cost'

export default function InsightsPage() {
  const { model, state } = useInsights()
  useEffect(() => { if (state.phase === 'idle') void model.load('24h') }, [model, state.phase])
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
          <div className={styles.periods} role="group" aria-label="Insights period">
            {periods.map((period) => (
              <button
                key={period}
                type="button"
                data-active={state.period === period}
                aria-pressed={state.period === period}
                disabled={loading}
                onClick={() => void model.load(period)}
              >
                {period === 'all' ? 'All' : period}
              </button>
            ))}
          </div>
          <PriceEditor
            prices={state.prices}
            phase={state.pricesPhase}
            knownModels={(report?.models ?? []).map((entry) => entry.model)}
            onSave={(draft) => model.savePrice(draft)}
            onRemove={(entry) => model.removePrice(entry)}
          />
          <button type="button" className={styles.refresh} disabled={loading} onClick={() => void model.load(state.period)}>
            <RefreshCw size={15} aria-hidden="true" />{loading ? 'Refreshing…' : 'Refresh'}
          </button>
        </div>
      </header>
      {state.error ? <div className={styles.error} role="alert">{state.error}</div> : null}
      {showSkeleton && loading ? <p className={styles.loading} role="status">Loading insights…</p> : null}
      <div className={styles.metrics} aria-busy={loading}>
        {showSkeleton
          ? skeletonMetrics.map((label) => (
            <div key={label} className={styles.metric} aria-hidden="true"><span>{label}</span><strong className={styles.skeleton} /></div>
          ))
          : (
            <>
              <Metric label="Requests" value={formatInteger(overview?.volume.requests)} detail={`${formatInteger(overview?.volume.completed)} completed · ${formatInteger(overview?.volume.failed)} failed`} />
              <Metric label="Success rate" value={overview ? `${Math.round(overview.successRate * 100)}%` : '—'} detail={`${formatInteger(overview?.volume.retries)} retries${overview?.topErrorCode ? ` · ${overview.topErrorCode}` : ''}`} />
              <Metric
                label="Estimated cost"
                value={overview ? (overview.volume.isPriced ? formatCost(overview.volume.cost) : formatPartialCost(overview.volume.cost)) : '—'}
                detail={overview?.volume.isPriced ? 'priced estimate' : `${pricedShare}% of requests priced`}
              />
              <Metric label="p95 latency" value={formatDuration(overview?.p95Ms ?? 0)} detail={`p50 ${formatDuration(overview?.p50Ms ?? 0)}`} />
              <Metric label="Tokens" value={formatInteger(overview?.volume.totalTokens)} detail={`${formatInteger(overview?.volume.cachedTokens)} cached · ${formatInteger(overview?.volume.reasoningTokens)} reasoning`} />
            </>
          )}
      </div>
      {report && report.daily.length > 1 ? (
        <section className={styles.panel} aria-label="Daily usage">
          <header><div><h2>Daily usage</h2><span>Requests and estimated cost per day</span></div></header>
          <DailyChart daily={report.daily} />
        </section>
      ) : null}
      {report && report.providers.length > 0 ? (
        <section className={styles.panel} aria-label="Provider breakdown">
          <header><div><h2>Providers</h2><span>Reliability and speed per provider — click a column to sort</span></div></header>
          <div className={styles.tableWrap}><table aria-label="Provider breakdown"><thead><tr>
            <SortHeader<ProviderSortKey> label="Provider" sortKey="name" active={providerSort} onSort={setProviderSort} />
            <SortHeader<ProviderSortKey> label="Requests" sortKey="requests" active={providerSort} onSort={setProviderSort} />
            <SortHeader<ProviderSortKey> label="Success" sortKey="success" active={providerSort} onSort={setProviderSort} />
            <SortHeader<ProviderSortKey> label="p50 / p95" sortKey="latency" active={providerSort} onSort={setProviderSort} />
            <SortHeader<ProviderSortKey> label="Retries" sortKey="retries" active={providerSort} onSort={setProviderSort} />
            <SortHeader<ProviderSortKey> label="Tokens" sortKey="tokens" active={providerSort} onSort={setProviderSort} />
            <SortHeader<ProviderSortKey> label="Est. cost" sortKey="cost" active={providerSort} onSort={setProviderSort} />
          </tr></thead><tbody>
            {providers.map((provider) => (
              <tr key={provider.id}>
                <td title={provider.id}>{provider.name || provider.id}</td>
                <td>{formatInteger(provider.volume.requests)}</td>
                <td>{formatRate(provider.volume.completed, provider.volume.requests)}</td>
                <td>{formatDuration(provider.p50Ms)} / {formatDuration(provider.p95Ms)}</td>
                <td>{formatInteger(provider.volume.retries)}</td>
                <td>{formatInteger(provider.volume.totalTokens)}</td>
                <td>{provider.volume.isPriced ? formatCost(provider.volume.cost) : formatPartialCost(provider.volume.cost)}</td>
              </tr>
            ))}
          </tbody></table></div>
        </section>
      ) : null}
      {report && report.models.length > 0 ? (
        <section className={styles.panel} aria-label="Model breakdown">
          <header><div><h2>Models</h2><span>What the tokens were spent on — click a column to sort</span></div></header>
          <div className={styles.tableWrap}><table aria-label="Model breakdown"><thead><tr>
            <SortHeader<ModelSortKey> label="Model" sortKey="model" active={modelSort} onSort={setModelSort} />
            <SortHeader<ModelSortKey> label="Requests" sortKey="requests" active={modelSort} onSort={setModelSort} />
            <SortHeader<ModelSortKey> label="Success" sortKey="success" active={modelSort} onSort={setModelSort} />
            <SortHeader<ModelSortKey> label="Tokens" sortKey="tokens" active={modelSort} onSort={setModelSort} />
            <SortHeader<ModelSortKey> label="Tok/s" sortKey="tps" active={modelSort} onSort={setModelSort} />
            <SortHeader<ModelSortKey> label="Est. cost" sortKey="cost" active={modelSort} onSort={setModelSort} />
          </tr></thead><tbody>
            {models.map((entry) => (
              <tr key={entry.model}>
                <td title={entry.model}>{entry.model}</td>
                <td>{formatInteger(entry.volume.requests)}</td>
                <td>{formatRate(entry.volume.completed, entry.volume.requests)}</td>
                <td>{formatInteger(entry.volume.totalTokens)}</td>
                <td>{entry.tokensPerSecond > 0 ? formatDecimal(entry.tokensPerSecond) : '—'}</td>
                <td>{entry.volume.isPriced ? formatCost(entry.volume.cost) : formatPartialCost(entry.volume.cost)}</td>
              </tr>
            ))}
          </tbody></table></div>
        </section>
      ) : null}
      {report && report.errors.length > 0 ? (
        <section className={styles.panel} aria-label="Error breakdown">
          <header><div><h2>Failures</h2><span>The relay's own error codes, ranked</span></div></header>
          <div className={styles.errorBars}>
            {report.errors.map((error) => {
              const share = report.overview.volume.requests > 0 ? 100 * error.requests / report.overview.volume.requests : 0
              return (
                <div key={error.errorCode} className={styles.errorBar}>
                  <span className={styles.errorCode}>{error.errorCode}</span>
                  <span className={styles.errorTrack} aria-hidden="true"><span className={styles.errorFill} style={{ width: `${Math.max(share, share > 0 ? 2 : 0)}%` }} /></span>
                  <span className={styles.errorCount}>{formatInteger(error.requests)}</span>
                </div>
              )
            })}
          </div>
        </section>
      ) : null}
      {report && report.unpricedModels.length > 0 ? (
        <section className={styles.panel} aria-label="Pricing gaps" data-testid="pricing-gaps">
          <header><div><h2><Coins size={14} aria-hidden="true" /> No price set</h2><span>The estimate does not cover these models</span></div></header>
          <p className={styles.unpriced}>{report.unpricedModels.join(' · ')}</p>
        </section>
      ) : null}
      {state.phase !== 'idle' || loading ? (
        <section className={styles.panel} aria-label="Recent persisted requests">
          <header><div><h2>Recent requests</h2><span>{state.recent.length > 0 ? `${state.recent.length} rows in ${state.period === 'all' ? 'history' : `the last ${state.period}`}` : 'No persisted requests in this period'}</span></div></header>
          <div className={styles.tableWrap}><table aria-label="Recent persisted requests"><thead><tr>
            <th scope="col">State</th><th scope="col">Model</th><th scope="col">Provider</th><th scope="col">HTTP</th><th scope="col">Latency</th><th scope="col">Processed</th><th scope="col">Cached</th><th scope="col">Time</th>
          </tr></thead><tbody>
            {loading && state.recent.length === 0
              ? [0, 1, 2].map((row) => <tr key={row} className={styles.skeletonRow} aria-hidden="true"><td colSpan={8}><span className={styles.skeleton} /></td></tr>)
              : state.recent.map((request) => (
                <tr key={request.id} data-failed={request.state === 'failed' || undefined}>
                  <td><span className={styles.state}><StatusDot state={request.state} />{stateLabels[request.state]}</span></td>
                  <td title={request.model}>{request.model || '—'}</td>
                  <td>{request.providerId || '—'}</td>
                  <td>{request.status || '—'}</td>
                  <td>{formatDuration(request.latencyMs)}</td>
                  <td>{formatInteger(request.totalTokens)}</td>
                  <td>{formatInteger(request.cachedTokens)}</td>
                  <td>{formatClock(request.updatedAt)}</td>
                </tr>
              ))}
            {!loading && state.recent.length === 0 ? <tr><td colSpan={8} className={styles.empty}>No persisted requests in this period.</td></tr> : null}
          </tbody></table></div>
        </section>
      ) : null}
      {failedRequests.length > 0 ? (
        <section className={styles.panel} aria-label="Request errors">
          <header><div><h2>Request errors</h2><span>Full diagnostics from the persisted rows</span></div></header>
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
        </section>
      ) : null}
    </section>
  )
}

function Metric({ label, value, detail }: { label: string; value: string; detail: string }) {
  return <div className={styles.metric}><span>{label}</span><strong>{value}</strong><small>{detail}</small></div>
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

function SortHeader<Key extends string>({ label, sortKey, active, onSort }: {
  label: string
  sortKey: Key
  active: { key: Key; descending: boolean }
  onSort: (key: Key) => void
}) {
  const isActive = active.key === sortKey
  return (
    <th
      scope="col"
      aria-sort={isActive ? (active.descending ? 'descending' : 'ascending') : 'none'}
    >
      <button type="button" className={styles.sortButton} data-active={isActive || undefined} onClick={() => onSort(sortKey)}>
        {label}
        <span aria-hidden="true" className={styles.sortArrow} data-visible={isActive || undefined}>{isActive && !active.descending ? '↑' : '↓'}</span>
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

function formatInteger(value: number | undefined): string {
  return (value ?? 0).toLocaleString()
}

function formatRate(part: number, total: number): string {
  if (total <= 0) return '—'
  return `${Math.round(100 * part / total)}%`
}

function successRate(volume: { requests: number; completed: number }): number {
  if (volume.requests <= 0) return 0
  return volume.completed / volume.requests
}

function formatDuration(ms: number): string {
  if (ms <= 0) return '—'
  if (ms < 1000) return `${Math.round(ms)}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
  return `${Math.round(ms / 60_000)}m`
}

function formatCost(cost: number | undefined): string {
  const value = cost ?? 0
  if (value === 0) return '$0'
  if (value < 0.01) return `$${value.toFixed(4)}`
  if (value < 1000) return `$${value.toFixed(2)}`
  return `$${Math.round(value).toLocaleString()}`
}

function formatPartialCost(cost: number): string {
  return cost > 0 ? `≥ ${formatCost(cost)}` : '—'
}

function formatDecimal(value: number): string {
  return value.toLocaleString(undefined, { maximumFractionDigits: 0 })
}

function previewError(value: string): string {
  return value.length > errorPreviewLimit ? `${value.slice(0, errorPreviewLimit)}…` : value
}
