/** Editable rows of the failover-chain wizard. The id exists for React only:
 *  providerId is not unique mid-edit (two rows can pick the same provider
 *  before submit rejects it), so it cannot key the rows — and an index key let
 *  a reorder hand one row's focus and IME state to a different entry. The id
 *  is minted when a row is created and travels with it through every move. */
export interface ChainEntry {
  readonly id: string
  readonly providerId: string
  upstreamModel: string
}

export function seedChainEntries(providers: readonly { id: string }[]): readonly ChainEntry[] {
  return providers.slice(0, 2).map((provider) => ({ id: crypto.randomUUID(), providerId: provider.id, upstreamModel: '' }))
}

/** The patch can never touch the id: the id is this module's whole reason to
 *  exist, and a caller rewriting it would silently reintroduce the state-loss
 *  the key invariant prevents. */
export type ChainEntryPatch = Partial<Pick<ChainEntry, 'providerId' | 'upstreamModel'>>

export function patchChainEntry(entries: readonly ChainEntry[], index: number, patch: ChainEntryPatch): readonly ChainEntry[] {
  return entries.map((entry, position) => position === index ? { ...entry, ...patch } : entry)
}

/** Swaps a row with its neighbor. Ids travel with the row so React moves the
 *  row's DOM node instead of rewriting a position's contents. */
export function moveChainEntry(entries: readonly ChainEntry[], index: number, direction: -1 | 1): readonly ChainEntry[] {
  const target = index + direction
  if (target < 0 || target >= entries.length) return entries
  const next = [...entries]
  const moved = next[target]
  next[target] = next[index]
  next[index] = moved
  return next
}

export function removeChainEntry(entries: readonly ChainEntry[], index: number): readonly ChainEntry[] {
  return entries.filter((_, position) => position !== index)
}

/** Appends the next provider no row uses yet, with a fresh id of its own. */
export function addChainEntry(entries: readonly ChainEntry[], providers: readonly { id: string }[]): readonly ChainEntry[] {
  const used = new Set(entries.map((entry) => entry.providerId))
  const provider = providers.find((candidate) => !used.has(candidate.id))
  return provider ? [...entries, { id: crypto.randomUUID(), providerId: provider.id, upstreamModel: '' }] : entries
}
