import { useMemo, useState } from 'react'
import type { InsightsDailyPoint } from '../domain/insights'
import { formatCost, formatDecimal, formatInteger } from '../../../shared/format/metrics'

type DailyMetric = 'requests' | 'success' | 'tokens' | 'cost' | 'rate'

type MetricDefinition = {
  id: DailyMetric
  label: string
  shortLabel: string
  description: string
}

const metricDefinitions: readonly MetricDefinition[] = [
  { id: 'requests', label: 'Requests', shortLabel: 'volume', description: 'Requests started per day' },
  { id: 'success', label: 'Success rate', shortLabel: 'reliability', description: 'Completed requests as a share of volume' },
  { id: 'tokens', label: 'Tokens', shortLabel: 'context', description: 'Input plus output tokens' },
  { id: 'cost', label: 'Estimated cost', shortLabel: 'spend', description: 'Priced request estimate' },
  { id: 'rate', label: 'Tokens / sec', shortLabel: 'throughput', description: 'Average generation rate' },
]

const chartHeight = 190
const chartWidth = 1000
const gridTicks = 4

/**
 * A small SVG keeps the dense daily view dependency-free. The selector changes
 * the unit rather than adding another chart, while the table remains the
 * canonical text alternative for exact values.
 */
export function DailyChart({ daily, currency = 'USD' }: { daily: readonly InsightsDailyPoint[]; currency?: string }) {
  const points = daily.slice(-90)
  const [metric, setMetric] = useState<DailyMetric>('requests')
  const [hovered, setHovered] = useState(-1)
  const definition = metricDefinitions.find((entry) => entry.id === metric) ?? metricDefinitions[0]
  const values = useMemo(() => points.map((point) => metricValue(point, metric)), [metric, points])
  const maxValue = Math.max(metric === 'success' ? 1 : 0.000001, ...values)
  const slot = points.length > 0 ? chartWidth / points.length : chartWidth
  const barWidth = Math.min(30, Math.max(3, slot * 0.62))
  const hoveredPoint = hovered >= 0 && hovered < points.length ? points[hovered] : null
  const peak = values.length > 0 ? Math.max(...values) : 0

  return (
    <figure className="daily-chart" aria-label={`Daily usage chart: ${definition.description}`}>
      <div className="daily-chart-toolbar">
        <div className="daily-selector" role="group" aria-label="Daily usage signal">
          {metricDefinitions.map((entry) => (
            <button
              key={entry.id}
              type="button"
              aria-pressed={metric === entry.id}
              className={metric === entry.id ? 'daily-selector-button daily-selector-button-active' : 'daily-selector-button'}
              onClick={() => { setMetric(entry.id); setHovered(-1) }}
            >
              <strong>{entry.label}</strong>
              <span>{entry.shortLabel}</span>
            </button>
          ))}
        </div>
        <div className="daily-peak" aria-live="polite">
          <span>Peak</span>
          <strong>{formatMetric(peak, metric, currency)}</strong>
        </div>
      </div>
      <div className="daily-plot-shell">
        <svg
          viewBox={`0 0 ${chartWidth} ${chartHeight}`}
          preserveAspectRatio="none"
          className="daily-svg"
          role="img"
          aria-label={`${definition.label}; ${points.length} days`}
          onMouseLeave={() => setHovered(-1)}
          onMouseMove={(event) => {
            if (points.length === 0) return
            const bounds = event.currentTarget.getBoundingClientRect()
            if (bounds.width <= 0) return
            const index = Math.floor(((event.clientX - bounds.left) / bounds.width) * points.length)
            setHovered(Math.max(0, Math.min(points.length - 1, index)))
          }}
        >
          {Array.from({ length: gridTicks + 1 }, (_, tick) => {
            const y = 8 + (chartHeight - 22) * (tick / gridTicks)
            return <line key={tick} x1={0} x2={chartWidth} y1={y} y2={y} className="daily-grid" />
          })}
          {points.map((point, index) => {
            const value = values[index] ?? 0
            const height = (chartHeight - 28) * (value / maxValue)
            const failedShare = point.volume.requests > 0 ? point.volume.failed / point.volume.requests : 0
            const failedHeight = metric === 'requests' ? Math.min(height, height * failedShare) : 0
            const bodyHeight = height - failedHeight
            const x = slot * index + (slot - barWidth) / 2
            const y = chartHeight - 12 - bodyHeight
            const hoveredClass = index === hovered ? ' daily-bar-hovered' : ''
            return (
              <g key={point.date}>
                {bodyHeight > 0 ? <rect x={x} y={y} width={barWidth} height={bodyHeight} rx={Math.min(3, barWidth / 2)} className={`daily-bar daily-bar-${metric}${hoveredClass}`} /> : null}
                {failedHeight > 0.5 ? <rect x={x} y={chartHeight - 12 - failedHeight - bodyHeight} width={barWidth} height={failedHeight} rx={Math.min(2, barWidth / 2)} className={`daily-bar daily-bar-failed${hoveredClass}`} /> : null}
              </g>
            )
          })}
          {hovered >= 0 && hovered < points.length ? (
            <line x1={slot * hovered + slot / 2} x2={slot * hovered + slot / 2} y1={0} y2={chartHeight - 10} className="daily-guide" />
          ) : null}
        </svg>
      </div>
      <div className="daily-axis" aria-hidden="true">
        {points.filter((_, index) => index % Math.max(1, Math.ceil(points.length / 8)) === 0).map((point) => <span key={point.date}>{point.date.slice(5)}</span>)}
      </div>
      <div className="daily-readout" data-empty={!hoveredPoint || undefined} aria-live="polite">
        {hoveredPoint ? (
          <>
            <strong>{hoveredPoint.date}</strong>
            <span>{formatInteger(hoveredPoint.volume.requests)} requests</span>
            <span>{formatRate(hoveredPoint.successRate)} success</span>
            <span>{formatInteger(hoveredPoint.volume.totalTokens)} tokens</span>
            <span>{formatDecimal(hoveredPoint.tokensPerSecond, 1)} tok/s</span>
            <span>{hoveredPoint.volume.cost > 0 ? formatCost(hoveredPoint.volume.cost, currency) : 'no cost data'}</span>
          </>
        ) : 'Hover a day for exact numbers'}
      </div>
      <figcaption className="daily-caption">
        <span className="daily-legend"><span className={`daily-swatch daily-swatch-${metric}`} aria-hidden="true" />{definition.label}</span>
        {metric === 'requests' ? <span className="daily-legend"><span className="daily-swatch daily-swatch-failed" aria-hidden="true" />failed share</span> : null}
        <span className="daily-caption-note">Showing the last {points.length || 0} days</span>
      </figcaption>
      <details className="daily-numbers">
        <summary>Exact numbers</summary>
        <div className="daily-table-wrap">
          <table>
            <thead><tr><th scope="col">Day</th><th scope="col">Requests</th><th scope="col">Success</th><th scope="col">Tokens</th><th scope="col">Tok/s</th><th scope="col">Est. cost</th></tr></thead>
            <tbody>
              {points.map((point) => (
                <tr key={point.date}>
                  <td>{point.date}</td>
                  <td>{formatInteger(point.volume.requests)}</td>
                  <td>{formatRate(point.successRate)}</td>
                  <td>{formatInteger(point.volume.totalTokens)}</td>
                  <td>{formatDecimal(point.tokensPerSecond, 1)}</td>
                  <td>{point.volume.cost > 0 ? formatCost(point.volume.cost, currency) : '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </details>
    </figure>
  )
}

function metricValue(point: InsightsDailyPoint, metric: DailyMetric): number {
  switch (metric) {
    case 'success': return point.successRate
    case 'tokens': return point.volume.totalTokens
    case 'cost': return point.volume.cost
    case 'rate': return point.tokensPerSecond
    default: return point.volume.requests
  }
}

function formatMetric(value: number, metric: DailyMetric, currency: string): string {
  if (metric === 'success') return formatRate(value)
  if (metric === 'cost') return formatCost(value, currency)
  if (metric === 'rate') return `${formatDecimal(value, 1)} tok/s`
  return formatInteger(value)
}

function formatRate(value: number): string {
  if (!Number.isFinite(value)) return '—'
  return `${Math.round(value * 100)}%`
}
