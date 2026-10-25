import { describe, expect, it } from 'vitest'
import type { CommandContribution } from '../shared/contracts/extensions'
import { scoreCommand } from './command-score'
import { isPluginRoute, navigation, routeFromHash, visibleNavigation } from './navigation'
import { railGroups, railItemName } from './rail'

const command = (title: string, group = 'Navigate', keywords: string[] = []): CommandContribution => ({ id: title, title, group, keywords, run: () => undefined })

describe('scoreCommand', () => {
  it('matches every command for an empty query', () => {
    expect(scoreCommand('  ', command('Overview'))).toBeGreaterThan(0)
  })

  it('needs every query character, in order', () => {
    expect(scoreCommand('ovw', command('Overview'))).toBeGreaterThan(0)
    expect(scoreCommand('wvo', command('Overview'))).toBe(0)
  })

  it('ranks prefixes and word starts above scattered matches', () => {
    expect(scoreCommand('key', command('API Keys'))).toBeGreaterThan(scoreCommand('key', command('Disable proxy keepalive')))
    expect(scoreCommand('set', command('Settings'))).toBeGreaterThan(scoreCommand('set', command('Reset sidebar')))
  })

  it('ranks a title match above a keyword match', () => {
    expect(scoreCommand('theme', command('Theme: Ink'))).toBeGreaterThan(scoreCommand('theme', command('Appearance', 'Navigate', ['theme'])))
  })
})

describe('navigation', () => {
  it('gives every static page a unique id and Alt+digit key', () => {
    const items = navigation.flatMap((section) => section.items)
    expect(new Set(items.map((item) => item.id)).size).toBe(items.length)
    const keys = items.flatMap((item) => (item.key ? [item.key] : []))
    expect(new Set(keys).size).toBe(keys.length)
  })

  it('hides capability-gated pages the control plane does not advertise', () => {
    const ids = (capabilities: string[]) => visibleNavigation(capabilities).flatMap((section) => section.items.map((item) => item.id))
    expect(ids([])).not.toContain('plugins')
    expect(ids(['plugins.manage'])).toContain('plugins')
    expect(ids([])).toContain('appearance')
  })

  it('reads static and plugin routes from the hash and sends anything else to the overview', () => {
    expect(routeFromHash('#appearance')).toBe('appearance')
    expect(routeFromHash('#/settings')).toBe('settings')
    expect(routeFromHash('#plugin/latency-lens/main')).toBe('plugin/latency-lens/main')
    expect(routeFromHash('#plugin/../settings')).toBe('overview')
    expect(routeFromHash('#nowhere')).toBe('overview')
  })

  it('accepts plugin routes by shape only', () => {
    expect(isPluginRoute('plugin/acme.tools/board')).toBe(true)
    expect(isPluginRoute('plugin/Acme/board')).toBe(false)
    expect(isPluginRoute('plugin/acme')).toBe(false)
  })
})

describe('railGroups', () => {
  const sections = visibleNavigation(['plugins.manage'])

  it('is the static navigation, with Alt+digit hints in the titles, when no plugin adds a page', () => {
    const rail = railGroups(sections, [])
    expect(rail.map((group) => group.label)).toEqual(sections.map((section) => section.label))
    expect(rail[0].items[0]).toMatchObject({ route: 'overview', title: 'Overview (Alt+1)' })
    expect(rail.flatMap((group) => group.items).find((item) => item.route === 'tunnel')?.title).toBe('Tunnel')
  })

  it('slots plugin pages above the pinned configuration group, keeping settings last', () => {
    const rail = railGroups(sections, [{ id: 'latency-lens/main', title: 'Latency board', icon: 'gauge' }, { id: 'acme/odd', title: 'Odd', icon: 'not-an-icon' }])
    const labels = rail.map((group) => group.label)
    expect(labels.at(-2)).toBe('Extensions')
    expect(rail.at(-1)?.items.at(-1)?.route).toBe('settings')
    const extensions = rail.at(-2)
    expect(extensions?.items.map((item) => [item.route, item.title])).toEqual([['plugin/latency-lens/main', 'Latency board'], ['plugin/acme/odd', 'Odd']])
    // An unknown icon name still renders: it falls back to the neutral icon.
    expect(extensions?.items[1].icon).toBeTypeOf('object')
    expect(new Set(rail.map((group) => group.key)).size).toBe(rail.length)
  })

  it('keys every group by a stable section slug', () => {
    const rail = railGroups(sections, [{ id: 'acme/board', title: 'Board' }])
    expect(rail.map((group) => group.key)).toEqual(['watch', 'routing', 'safety', 'publishing', 'extensions', 'configure'])
  })

  it('carries a plugin page badge into the item, its title and its accessible name', () => {
    const rail = railGroups(sections, [
      { id: 'acme/queue', title: 'Queue', badge: { text: ' 3 ', tone: 'warning' } },
      { id: 'acme/new', title: 'News', badge: { text: 'new' } },
      { id: 'acme/blank', title: 'Blank', badge: { text: '   ' } },
    ])
    const [queue, news, blank] = rail.find((group) => group.key === 'extensions')?.items ?? []
    expect(queue.badge).toEqual({ text: '3', tone: 'warning' })
    expect(queue.title).toBe('Queue (3)')
    expect(railItemName(queue)).toBe('Queue, 3')
    expect(news.badge).toEqual({ text: 'new', tone: 'neutral' })
    // A badge with nothing to say is no badge.
    expect(blank.badge).toBeUndefined()
    expect(railItemName(blank)).toBe('Blank')
  })
})
