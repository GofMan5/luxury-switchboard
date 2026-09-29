import type { InsightsDailyPoint } from '../domain/insights'

// Hand-rolled SVG, no chart dependency: two series (requests as bars, cost as
// a line on a second axis), a shared grid, and a text alternative for every
// point so the numbers are not locked inside pixels. Widths are computed, not
// measured, so the chart survives any container size without a resize
// observer.
const chartHeight = 180
const chartWidth = 1000
const costAxisMax = 4 // $ ticks: 0, 25, 50, 75, 100% of the max

export function DailyChart({ daily }: { daily: readonly InsightsDailyPoint[] }) {
  const points = daily.slice(-90)
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
  return (
    <figure className="daily-chart" role="img" aria-label={`Daily requests up to ${maxRequests.toLocaleString()} and estimated cost up to $${maxCost < 0.01 ? maxCost.toFixed(4) : maxCost.toFixed(2)}`}>
      <svg viewBox={`0 0 ${chartWidth} ${chartHeight}`} preserveAspectRatio="none" className="daily-svg" aria-hidden="true">
        {Array.from({ length: costAxisMax + 1 }, (_, tick) => {
          const y = 4 + (chartHeight - 18) * (tick / costAxisMax)
          return <line key={tick} x1={0} x2={chartWidth} y1={y} y2={y} className="daily-grid" />
        })}
        {costPath ? <path d={costPath} className="daily-cost-area" /> : null}
        {costPath ? <path d={costLine} className="daily-cost-line" /> : null}
        {points.map((point, index) => {
          const height = (chartHeight - 18) * (point.volume.requests / maxRequests)
          return <rect key={point.date} x={slot * index + (slot - barWidth) / 2} y={chartHeight - 6 - height} width={barWidth} height={height} rx={Math.min(3, barWidth / 2)} className={point.volume.failed > 0 ? 'daily-bar daily-bar-failed' : 'daily-bar'} />
        })}
      </svg>
      <div className="daily-axis" aria-hidden="true">
        {points.filter((_, index) => index % Math.ceil(points.length / 8) === 0).map((point) => (
          <span key={point.date}>{point.date.slice(5)}</span>
        ))}
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
                <td>{point.volume.cost > 0 ? `$${point.volume.cost.toFixed(2)}` : '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </details>
    </figure>
  )
}
