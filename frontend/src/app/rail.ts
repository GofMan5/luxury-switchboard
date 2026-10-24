import type { PageContribution } from '../shared/contracts/extensions'
import type { AppRoute, NavigationItem, NavigationSection } from './navigation'
import { pluginIcon } from './plugin-icons'

export interface RailItem {
  readonly route: AppRoute
  readonly label: string
  readonly icon: NavigationItem['icon']
  readonly title: string
}

export interface RailGroup {
  readonly key: string
  readonly label: string
  readonly items: readonly RailItem[]
}

/** One rail, two sources: static pages and plugin pages. The last static group
 * (appearance, plugins, settings) stays pinned to the foot of the rail, after
 * any plugin pages — in DOM order too, so Tab follows what the eye sees. */
export function railGroups(sections: readonly NavigationSection[], pages: readonly PageContribution[]): readonly RailGroup[] {
  const groups: RailGroup[] = sections.map((section, index) => ({
    key: section.label || `section-${index}`,
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
    items: pages.map((page) => ({ route: `plugin/${page.id}` as AppRoute, label: page.title, icon: pluginIcon(page.icon), title: page.title })),
  }
  return groups.length === 0 ? [extensions] : [...groups.slice(0, -1), extensions, ...groups.slice(-1)]
}
