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

// Symbols for the currencies people actually bill in; an unlisted ISO code
// renders as a suffix instead of a wrong glyph.
const currencySymbols: Record<string, string> = {
  USD: '$', EUR: '€', GBP: '£', JPY: '¥', CNY: '¥', KRW: '₩', RUB: '₽', INR: '₹',
  TRY: '₺', BRL: 'R$', PLN: 'zł', UAH: '₴', KZT: '₸', ILS: '₪', THB: '฿', VND: '₫',
  HKD: 'HK$', SGD: 'S$', TWD: 'NT$', AUD: 'A$', CAD: 'C$',
}

/** Money as the operator reads it: cents under one unit, round numbers above a
 * thousand, in the catalog's currency of account. */
export function formatCost(cost: number | undefined, currency = 'USD'): string {
  const value = cost ?? 0
  if (!Number.isFinite(value)) return '—'
  const symbol = currencySymbols[currency] ?? ''
  const suffix = symbol ? '' : ` ${currency}`
  if (value === 0) return `${symbol}0${suffix}`
  if (value < 0.01) return `${symbol}${value.toFixed(4)}${suffix}`
  if (value < 1_000) return `${symbol}${value.toFixed(2)}${suffix}`
  return `${symbol}${Math.round(value).toLocaleString()}${suffix}`
}

export function formatClock(value: string): string {
  const date = new Date(value)
  // A zero timestamp means "never seen", not the year 1: never invent a clock for it.
  if (Number.isNaN(date.valueOf()) || date.valueOf() <= 0) return '—'
  return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}
