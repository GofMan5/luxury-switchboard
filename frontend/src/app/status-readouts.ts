import type { StatusContribution } from '../shared/contracts/extensions'

/** The most readouts the runtime bar shows inline, at full width. */
export const STATUS_LIMIT = 4

/** How many fit inline at a window width: the bar gives way to the route and
 * the relay controls first, so narrow windows keep one readout, and below the
 * supported size every readout lives behind the "+N" pill. */
export function readoutLimit(compact: boolean, tiny: boolean): number {
  return tiny ? 0 : compact ? 1 : STATUS_LIMIT
}

/** Inline readouts first, the rest behind the "+N" pill, in contribution order. */
export function splitReadouts(status: readonly StatusContribution[], limit: number) {
  return { shown: status.slice(0, limit), hidden: status.slice(limit) }
}

/** The "+N" pill's accessible name and tooltip: every hidden readout, so the
 * count is never the whole answer. */
export function overflowSummary(hidden: readonly StatusContribution[]): string {
  return `${hidden.length} more ${hidden.length === 1 ? 'readout' : 'readouts'}: ${hidden.map((readout) => readout.text).join(', ')}`
}

/** Who contributed a readout: its id up to the last colon (`plugin:acme` for
 * `plugin:acme:latency`). Exposed as `data-owner` so a stylesheet can address
 * one owner's readouts without matching on their text. */
export function readoutOwner(id: string): string {
  const at = id.lastIndexOf(':')
  return at > 0 ? id.slice(0, at) : ''
}
