export function formatDuration(milliseconds: number): string {
  if (!Number.isFinite(milliseconds) || milliseconds <= 0) return '—'
  if (milliseconds < 1_000) return `${Math.round(milliseconds)} ms`
  return `${(milliseconds / 1_000).toFixed(milliseconds < 10_000 ? 2 : 1)} s`
}

export function formatDecimal(value: number, digits = 1): string {
  if (!Number.isFinite(value)) return '—'
  return value.toLocaleString(undefined, {
    minimumFractionDigits: digits,
    maximumFractionDigits: digits,
  })
}

export function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return '—'
  if (value < 1_024) return `${value} B`
  if (value < 1_048_576) return `${(value / 1_024).toFixed(1)} KiB`
  return `${(value / 1_048_576).toFixed(1)} MiB`
}

export function formatInteger(value: number | undefined): string {
  if (value === undefined || !Number.isFinite(value)) return '—'
  return value.toLocaleString()
}

/** Money as the operator reads it: cents under a dollar, whole dollars above a thousand. */
export function formatCost(cost: number | undefined): string {
  const value = cost ?? 0
  if (!Number.isFinite(value)) return '—'
  if (value === 0) return '$0'
  if (value < 0.01) return `$${value.toFixed(4)}`
  if (value < 1_000) return `$${value.toFixed(2)}`
  return `$${Math.round(value).toLocaleString()}`
}

export function formatClock(value: string): string {
  const date = new Date(value)
  // A zero timestamp means "never seen", not the year 1: never invent a clock for it.
  if (Number.isNaN(date.valueOf()) || date.valueOf() <= 0) return '—'
  return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}
