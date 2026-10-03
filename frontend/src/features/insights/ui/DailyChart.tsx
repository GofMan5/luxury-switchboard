import { useState } from 'react'
import type { InsightsDailyPoint } from '../domain/insights'
import { formatCost, formatInteger } from '../../../shared/format/metrics'

// Hand-rolled SVG, no chart dependency: two series (requests as bars, cost as
// a line on a second axis), a shared grid, and a text alternative for every
// point so the numbers are not locked inside pixels. Widths are computed, not
// measured, so the chart survives any container size without a resize
// observer.
const chartHeight = 180
const chartWidth = 1000
const costAxisMax = 4 // $ ticks: 0, 25, 50, 75, 100% of the max

export function DailyChart({ daily, currency = 'USD' }: { daily: readonly InsightsDailyPoint[]; currency?: string }) {
  const points = daily.slice(-90)
  const [hovered, setHovered] = useState(-1)
  const maxRequests = Math.max(1, ...points.map((point) => point.volume.requests))
  const maxCost = Math.max(0.000001, ...points.map((point) => point.volume.cost))
  const slot = points.length > 0 ? chartWidth / points.length : chartWidth
  const barWidth = Math.min(28, Math.max(3, slot * 0.62))
  const costLine = points
    .map((point, index) => {
      const x = slot * index + slot / 2
      const y = chartHeight - (chartHeight - 10) * (point.volume.cost / maxCost) - 4
      return `${index === 0 ? 'M' : 'L'}${x.toFixed(1)},${y.toFixed(1)}`
    })
    .join(' ')
  const costPath = points.length > 1 ? `M${(chartHeight - 4).toFixed(1)},${(chartHeight - 4).toFixed(1)} ${costLine.replace(/^M/, 'L')} L${(chartWidth - slot / 2).toFixed(1)},${(chartHeight - 4).toFixed(1)} Z` : ''
  const hoveredPoint = hovered >= 0 && hovered < points.length ? points[hovered] : null

  return (
    <figure className="daily-chart" role="img" aria-label={`Daily requests up to ${maxRequests.toLocaleString()} and estimated cost up to ${formatCost(maxCost, currency)}`}>
      <svg
        viewBox={`0 0 ${chartWidth} ${chartHeight}`}
        preserveAspectRatio="none"
        className="daily-svg"
        aria-hidden="true"
        onMouseLeave={() => setHovered(-1)}
        onMouseMove={(event) => {
          const bounds = event.currentTarget.getBoundingClientRect()
          if (bounds.width <= 0) return
          const index = Math.floor(((event.clientX - bounds.left) / bounds.width) * points.length)
          setHovered(Math.max(0, Math.min(points.length - 1, index)))
        }}
      >
        {Array.from({ length: costAxisMax + 1 }, (_, tick) => {
          const y = 4 + (chartHeight - 18) * (tick / costAxisMax)
          return <line key={tick} x1={0} x2={chartWidth} y1={y} y2={y} className="daily-grid" />
        })}
        {costPath ? <path d={costPath} className="daily-cost-area" /> : null}
        {costPath ? <path d={costLine} className="daily-cost-line" /> : null}
        {points.map((point, index) => {
          // Stacked, not tinted: failures are the red cap on top of the
          // completed share, so a day with three failures out of two hundred
          // reads as a good day with a nick, not as a red bar.
          const height = (chartHeight - 18) * (point.volume.requests / maxRequests)
          const failedShare = point.volume.requests > 0 ? point.volume.failed / point.volume.requests : 0
          const failedHeight = Math.min(height, height * failedShare)
          const bodyHeight = height - failedHeight
          const x = slot * index + (slot - barWidth) / 2
          const hoveredClass = index === hovered ? ' daily-bar-hovered' : ''
          return (
            <g key={point.date}>
              {bodyHeight > 0 ? (
                <rect x={x} y={chartHeight - 6 - bodyHeight} width={barWidth} height={bodyHeight} rx={Math.min(3, barWidth / 2)} className={`daily-bar${hoveredClass}`} />
              ) : null}
              {failedHeight > 0.5 ? (
                <rect x={x} y={chartHeight - 6 - bodyHeight - failedHeight} width={barWidth} height={failedHeight} rx={Math.min(2, barWidth / 2)} className={`daily-bar daily-bar-failed${hoveredClass}`} />
              ) : null}
            </g>
          )
        })}
        {hovered >= 0 && hovered < points.length ? (
          <line
            x1={slot * hovered + slot / 2}
            x2={slot * hovered + slot / 2}
            y1={0}
            y2={chartHeight - 4}
            className="daily-guide"
          />
        ) : null}
      </svg>
      <div className="daily-axis" aria-hidden="true">
        {points.filter((_, index) => index % Math.ceil(points.length / 8) === 0).map((point) => (
          <span key={point.date}>{point.date.slice(5)}</span>
        ))}
      </div>
      {/* The hover answer doubles as the "is it alive" cue: at rest it invites,
          over a bar it reports. Screen readers get the numbers table instead. */}
      <div className="daily-readout" data-empty={!hoveredPoint || undefined}>
        {hoveredPoint
          ? `${hoveredPoint.date} · ${formatInteger(hoveredPoint.volume.requests)} requests · ${formatInteger(hoveredPoint.volume.failed)} failed · ${hoveredPoint.volume.cost > 0 ? formatCost(hoveredPoint.volume.cost, currency) : 'no cost data'}`
          : 'Hover a day for exact numbers'}
      </div>
      <figcaption className="daily-caption">
        <span className="daily-legend"><span className="daily-swatch daily-swatch-bar" aria-hidden="true" />requests</span>
        <span className="daily-legend"><span className="daily-swatch daily-swatch-cost" aria-hidden="true" />estimated cost</span>
      </figcaption>
      <details className="daily-numbers">
        <summary>Exact numbers</summary>
        <table>
          <thead><tr><th scope="col">Day</th><th scope="col">Requests</th><th scope="col">Failed</th><th scope="col">Est. cost</th></tr></thead>
          <tbody>
            {points.map((point) => (
              <tr key={point.date}>
                <td>{point.date}</td>
                <td>{point.volume.requests.toLocaleString()}</td>
                <td>{point.volume.failed.toLocaleString()}</td>
                <td>{point.volume.cost > 0 ? formatCost(point.volume.cost, currency) : '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </details>
    </figure>
  )
}
