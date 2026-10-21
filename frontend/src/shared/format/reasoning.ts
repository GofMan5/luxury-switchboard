// The sidecar allowlists these names; anything else would come from a newer
// sidecar and is shown as sent rather than hidden.
const effortNames: Readonly<Record<string, string>> = {
  none: 'Off',
  minimal: 'Minimal',
  low: 'Low',
  medium: 'Medium',
  high: 'High',
  xhigh: 'X-High',
  max: 'Max',
  auto: 'Auto',
  on: 'On',
}

function effortName(effort: string | undefined): string {
  if (!effort) return ''
  return effortNames[effort] ?? effort.slice(0, 12)
}

function compactTokens(value: number): string {
  if (value < 1_000) return String(Math.round(value))
  const thousands = value / 1_000
  return `${thousands < 10 ? Number(thousands.toFixed(1)) : Math.round(thousands)}K`
}

function validBudget(budget: number | undefined): number {
  return budget !== undefined && Number.isFinite(budget) && budget > 0 ? budget : 0
}

/** The requested reasoning level as a table badge: `High`, `16K`, `Low · 4K`;
 * empty when the request left reasoning to the provider. */
export function formatReasoningLevel(effort: string | undefined, budget: number | undefined): string {
  const name = effortName(effort)
  const tokens = validBudget(budget)
  if (!tokens) return name
  // "On" says nothing a budget does not already say.
  if (!name || effort === 'on') return compactTokens(tokens)
  return `${name} · ${compactTokens(tokens)}`
}

/** The same level in words, for details and tooltips. */
export function describeReasoningLevel(effort: string | undefined, budget: number | undefined): string {
  const name = effortName(effort)
  const tokens = validBudget(budget)
  const budgetText = tokens ? `${tokens.toLocaleString()}-token budget` : ''
  if (effort === 'none') return 'Off'
  if (effort === 'auto') return budgetText ? `Adaptive · ${budgetText}` : 'Adaptive'
  if (!name || effort === 'on') return budgetText || (name ? 'On' : '')
  return budgetText ? `${name} effort · ${budgetText}` : `${name} effort`
}
