import { useEffect } from 'react'
import { Coins, RefreshCw } from 'lucide-react'
import type { InsightsPeriod } from '../domain/insights'
import { useInsights } from './useInsights'
import { PriceEditor } from './PriceEditor'
import { DailyChart } from './DailyChart'
import styles from './InsightsPage.module.css'

const periods: readonly InsightsPeriod[] = ['24h', '48h', '72h', 'all']

const skeletonMetrics = ['Requests', 'Success rate', 'Estimated cost', 'p95 latency', 'Generation speed']

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
          <header><div><h2>Providers</h2><span>Reliability and speed per provider</span></div></header>
          <div className={styles.tableWrap}><table aria-label="Provider breakdown"><thead><tr>
            <th scope="col">Provider</th><th scope="col">Requests</th><th scope="col">Success</th>
            <th scope="col">p50 / p95</th><th scope="col">Retries</th><th scope="col">Tokens</th><th scope="col">Est. cost</th>
          </tr></thead><tbody>
            {report.providers.map((provider) => (
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
          <header><div><h2>Models</h2><span>What the tokens were spent on</span></div></header>
          <div className={styles.tableWrap}><table aria-label="Model breakdown"><thead><tr>
            <th scope="col">Model</th><th scope="col">Requests</th><th scope="col">Success</th>
            <th scope="col">Tokens</th><th scope="col">Tok/s</th><th scope="col">Est. cost</th>
          </tr></thead><tbody>
            {report.models.map((model) => (
              <tr key={model.model}>
                <td title={model.model}>{model.model}</td>
                <td>{formatInteger(model.volume.requests)}</td>
                <td>{formatRate(model.volume.completed, model.volume.requests)}</td>
                <td>{formatInteger(model.volume.totalTokens)}</td>
                <td>{model.tokensPerSecond > 0 ? formatDecimal(model.tokensPerSecond) : '—'}</td>
                <td>{model.volume.isPriced ? formatCost(model.volume.cost) : formatPartialCost(model.volume.cost)}</td>
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
    </section>
  )
}

function Metric({ label, value, detail }: { label: string; value: string; detail: string }) {
  return <div className={styles.metric}><span>{label}</span><strong>{value}</strong><small>{detail}</small></div>
}

function formatInteger(value: number | undefined): string {
  return (value ?? 0).toLocaleString()
}

function formatRate(part: number, total: number): string {
  if (total <= 0) return '—'
  return `${Math.round(100 * part / total)}%`
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
