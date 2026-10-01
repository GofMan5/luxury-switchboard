import { describe, expect, it } from 'vitest'
import { addChainEntry, moveChainEntry, patchChainEntry, removeChainEntry, seedChainEntries } from './chain-entries'

const providers = [{ id: 'alpha' }, { id: 'beta' }, { id: 'gamma' }]

describe('chain entries', () => {
  it('mints a distinct id for every seeded row', () => {
    const entries = seedChainEntries(providers)
    expect(entries.map((entry) => entry.providerId)).toEqual(['alpha', 'beta'])
    const ids = entries.map((entry) => entry.id)
    expect(new Set(ids).size).toBe(ids.length)
  })

  it('moves a row without changing any id, so React keeps each row its own DOM', () => {
    const seeded = seedChainEntries(providers)
    const entries = addChainEntry(seeded, providers)
    const moved = moveChainEntry(entries, 2, -1)
    expect(moved.map((entry) => entry.providerId)).toEqual(['alpha', 'gamma', 'beta'])
    // Ids travel with the rows, not with the positions: the id that sat on the
    // third row is now on the second, and every original id is still present.
    expect(moved.map((entry) => entry.id)).toEqual([entries[0].id, entries[2].id, entries[1].id])
  })

  it('refuses to move past the ends of the list', () => {
    const entries = seedChainEntries(providers)
    expect(moveChainEntry(entries, 0, -1)).toBe(entries)
    expect(moveChainEntry(entries, 1, 1)).toBe(entries)
  })

  it('keeps the row id when its provider changes mid-edit', () => {
    const entries = seedChainEntries(providers)
    const patched = patchChainEntry(entries, 0, { providerId: 'gamma', upstreamModel: '' })
    expect(patched[0].id).toBe(entries[0].id)
    expect(patched[0].providerId).toBe('gamma')
  })

  it('adds the next unused provider with a fresh id', () => {
    const entries = seedChainEntries(providers)
    const grown = addChainEntry(entries, providers)
    expect(grown.map((entry) => entry.providerId)).toEqual(['alpha', 'beta', 'gamma'])
    expect(grown.slice(0, 2).map((entry) => entry.id)).toEqual(entries.map((entry) => entry.id))
    expect(new Set(grown.map((entry) => entry.id)).size).toBe(3)
    expect(addChainEntry(grown, providers)).toBe(grown)
  })

  it('removes a row without disturbing the ids of the rest', () => {
    const entries = addChainEntry(seedChainEntries(providers), providers)
    const pruned = removeChainEntry(entries, 1)
    expect(pruned.map((entry) => entry.providerId)).toEqual(['alpha', 'gamma'])
    expect(pruned.map((entry) => entry.id)).toEqual([entries[0].id, entries[2].id])
  })
})
