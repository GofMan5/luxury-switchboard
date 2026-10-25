import type { BadgeContribution, LayoutContribution, PageContribution } from '../shared/contracts/extensions'
import { isPluginRoute, type AppRoute, type NavigationItem, type NavigationSection } from './navigation'
import { pluginIcon } from './plugin-icons'

export interface RailItem {
  readonly route: AppRoute
  readonly label: string
  readonly icon: NavigationItem['icon']
  readonly title: string
  /** A plugin page's count or label; absent when there is nothing to say. */
  readonly badge?: BadgeContribution
}

export interface RailGroup {
  /** Stable slug: the React key and the rail section's `data-section`. */
  readonly key: string
  readonly label: string
  readonly items: readonly RailItem[]
}

/** One rail, two sources: static pages and plugin pages. The last static group
 * (appearance, plugins, settings) stays pinned to the foot of the rail, after
 * any plugin pages — in DOM order too, so Tab follows what the eye sees. */
export function railGroups(sections: readonly NavigationSection[], pages: readonly PageContribution[]): readonly RailGroup[] {
  const groups: RailGroup[] = sections.map((section) => ({
    key: section.id,
    label: section.label,
    items: section.items.map((item) => ({
      route: item.id,
      label: item.label,
      icon: item.icon,
      title: item.key ? `${item.label} (Alt+${item.key})` : item.label,
    })),
  }))
  if (pages.length === 0) return groups
  const extensions: RailGroup = {
    key: 'extensions',
    label: 'Extensions',
    items: pages.map((page) => {
      const badge = page.badge?.text.trim() ? { text: page.badge.text.trim(), tone: page.badge.tone ?? 'neutral' } : undefined
      return {
        route: `plugin/${page.id}` as AppRoute,
        label: page.title,
        icon: pluginIcon(page.icon),
        title: badge ? `${page.title} (${badge.text})` : page.title,
        ...(badge ? { badge } : {}),
      }
    }),
  }
  return groups.length === 0 ? [extensions] : [...groups.slice(0, -1), extensions, ...groups.slice(-1)]
}

/** The way back to a plugin's settings, or out of its layout, never leaves the rail. */
const ALWAYS_SHOWN: ReadonlySet<string> = new Set(['plugins', 'settings'])

/** Applies a plugin's layout to the built-in sections: hidden items go (never
 * plugins or settings), listed items lead their section in the listed order,
 * unlisted ones follow in their own order, and unknown ids are ignored. A
 * section left empty disappears. Alt+digit keys and the palette still reach
 * every page, so hiding is tidying, not a lock. */
export function arrangeSections(sections: readonly NavigationSection[], layout: Pick<LayoutContribution, 'hidden' | 'order'> | undefined): readonly NavigationSection[] {
  if (!layout) return sections
  const hidden = new Set(layout.hidden.filter((id) => !ALWAYS_SHOWN.has(id)))
  const rank = (id: string) => {
    const at = layout.order.indexOf(id)
    return at < 0 ? layout.order.length : at
  }
  return sections
    .map((section) => ({
      ...section,
      // Array.sort is stable, so unlisted items keep their relative order.
      items: section.items.filter((item) => !hidden.has(item.id)).sort((left, right) => rank(left.id) - rank(right.id)),
    }))
    .filter((section) => section.items.length > 0)
}

/** The route a layout's start page names, when it is one this window can show. */
export function layoutStartRoute(startPage: string | undefined, sections: readonly NavigationSection[]): AppRoute | undefined {
  if (!startPage) return undefined
  if (isPluginRoute(startPage)) return startPage
  return sections.some((section) => section.items.some((item) => item.id === startPage)) ? startPage as AppRoute : undefined
}

/** What a screen reader announces for a rail entry: the badge is part of the
 * name, because a collapsed rail shows it only as a dot. */
export function railItemName(item: RailItem): string {
  return item.badge ? `${item.label}, ${item.badge.text}` : item.label
}
