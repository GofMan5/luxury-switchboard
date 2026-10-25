import { describe, expect, it, vi } from 'vitest'
import { actionsFor, ExtensionPoints, slotsFor, type ActionContribution, type LayoutContribution, type SlotContribution } from './extensions'

const slot = (id: string, scope: SlotContribution['scope'], extra: Partial<SlotContribution> = {}): SlotContribution => ({
  id, owner: 'Acme', scope, title: id, render: () => null, ...extra,
})
const action = (id: string, scope: ActionContribution['scope']): ActionContribution => ({ id, owner: 'Acme', scope, title: id, run: () => undefined })
const layout = (owner: string, extra: Partial<LayoutContribution> = {}): LayoutContribution => ({ owner, hidden: [], order: [], reset: () => undefined, ...extra })

describe('ExtensionPoints slots, actions and layout', () => {
  it('merges slots and actions across owners and replaces an owner wholesale', () => {
    const points = new ExtensionPoints()
    points.contribute('plugin:a', { slots: [slot('plugin:a:w', 'overview')], actions: [action('plugin:a:x', 'key')] })
    points.contribute('plugin:b', { slots: [slot('plugin:b:t', 'activity')] })
    expect(points.snapshot().slots.map((entry) => entry.id)).toEqual(['plugin:a:w', 'plugin:b:t'])
    expect(points.snapshot().actions.map((entry) => entry.id)).toEqual(['plugin:a:x'])

    points.contribute('plugin:a', { slots: [] })
    expect(points.snapshot().slots.map((entry) => entry.id)).toEqual(['plugin:b:t'])
    // Kinds left out stay as they were.
    expect(points.snapshot().actions.map((entry) => entry.id)).toEqual(['plugin:a:x'])
    points.clear('plugin:a')
    expect(points.snapshot().actions).toEqual([])
  })

  it('orders a scope by order, then title, and filters actions by scope', () => {
    const points = new ExtensionPoints()
    points.contribute('plugin:a', {
      slots: [slot('c', 'overview'), slot('b', 'overview', { order: 2 }), slot('a', 'overview'), slot('z', 'overview', { order: 1 }), slot('t', 'provider')],
      actions: [action('k1', 'key'), action('h1', 'header'), action('k2', 'key')],
    })
    expect(slotsFor(points.snapshot(), 'overview').map((entry) => entry.id)).toEqual(['z', 'b', 'a', 'c'])
    expect(slotsFor(points.snapshot(), 'settings')).toEqual([])
    expect(actionsFor(points.snapshot(), 'key').map((entry) => entry.id)).toEqual(['k1', 'k2'])
  })

  it('lets the first layout owner win until it lets go', () => {
    const points = new ExtensionPoints()
    const listener = vi.fn()
    points.subscribe(listener)
    expect(points.snapshot().layout).toBeUndefined()

    points.contribute('plugin:a', { layout: layout('Alpha', { hidden: ['tests'] }) })
    points.contribute('plugin:b', { layout: layout('Beta') })
    expect(points.snapshot()).toMatchObject({ layoutOwner: 'plugin:a', layout: { owner: 'Alpha', hidden: ['tests'] } })

    // The holder updating its layout keeps its claim.
    points.contribute('plugin:a', { layout: layout('Alpha', { hidden: ['clients'] }) })
    expect(points.snapshot()).toMatchObject({ layoutOwner: 'plugin:a', layout: { hidden: ['clients'] } })
    // Contributing other kinds does not touch the claim.
    points.contribute('plugin:a', { pages: [] })
    expect(points.snapshot().layoutOwner).toBe('plugin:a')

    points.contribute('plugin:a', { layout: undefined })
    expect(points.snapshot()).toMatchObject({ layoutOwner: 'plugin:b', layout: { owner: 'Beta' } })
    points.clear('plugin:b')
    expect(points.snapshot().layout).toBeUndefined()
    expect(points.snapshot().layoutOwner).toBeUndefined()
    expect(listener).toHaveBeenCalledTimes(6)
  })

  it('moves a re-claimed layout to the back of the queue', () => {
    const points = new ExtensionPoints()
    points.contribute('plugin:a', { layout: layout('Alpha') })
    points.contribute('plugin:b', { layout: layout('Beta') })
    points.contribute('plugin:a', { layout: undefined })
    points.contribute('plugin:a', { layout: layout('Alpha') })
    expect(points.snapshot().layoutOwner).toBe('plugin:b')
  })
})
