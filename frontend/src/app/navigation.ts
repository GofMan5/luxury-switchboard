import type { ComponentType } from 'react'
import {
  Activity,
  CircleGauge,
  Coins,
  Database,
  FlaskConical,
  KeyRound,
  Palette,
  Puzzle,
  RadioTower,
  Route,
  Settings,
  ShieldAlert,
  UsersRound,
} from 'lucide-react'

export type StaticRoute =
  | 'overview'
  | 'activity'
  | 'providers'
  | 'keys'
  | 'routes'
  | 'tests'
  | 'guardrails'
  | 'tunnel'
  | 'clients'
  | 'insights'
  | 'appearance'
  | 'plugins'
  | 'settings'

/** A page a plugin registered: `plugin/<pluginId>/<pageId>`. */
export type PluginRoute = `plugin/${string}`

export type AppRoute = StaticRoute | PluginRoute

export interface NavigationItem {
  readonly id: StaticRoute
  readonly label: string
  readonly icon: ComponentType<{ size?: number; strokeWidth?: number }>
  /** Shown only when the control plane advertises this capability. */
  readonly capability?: string
  /** Alt+digit jumps here; the palette shows the chord. */
  readonly key?: string
}

export interface NavigationSection {
  /** Empty for the first section: no heading above the fold. */
  readonly label: string
  readonly items: readonly NavigationItem[]
}

// Twelve flat entries read as a wall; the same twelve in five named groups
// read as a map. The groups follow the operator's mental model: watch the
// traffic, tune the routing, inspect safety, publish, configure.
export const navigation: readonly NavigationSection[] = [
  {
    label: '',
    items: [
      { id: 'overview', label: 'Overview', icon: CircleGauge, key: '1' },
      { id: 'activity', label: 'Live Activity', icon: Activity, key: '2' },
      { id: 'insights', label: 'Insights', icon: Coins, key: '3' },
    ],
  },
  {
    label: 'Routing',
    items: [
      { id: 'providers', label: 'Providers', icon: Database, key: '4' },
      { id: 'keys', label: 'API Keys', icon: KeyRound, key: '5' },
      { id: 'routes', label: 'Model Routes', icon: Route, key: '6' },
      { id: 'tests', label: 'Tests', icon: FlaskConical, key: '7' },
    ],
  },
  {
    label: 'Safety',
    items: [
      // Everyone inspects provider answers. Every user has the same right
      // to know what a provider sent them as the owner does.
      { id: 'guardrails', label: 'Guardrails', icon: ShieldAlert, key: '8' },
    ],
  },
  {
    label: 'Publishing',
    items: [
      { id: 'tunnel', label: 'Tunnel', icon: RadioTower },
      { id: 'clients', label: 'Clients', icon: UsersRound },
    ],
  },
  {
    label: '',
    items: [
      { id: 'appearance', label: 'Appearance', icon: Palette },
      { id: 'plugins', label: 'Plugins', icon: Puzzle, capability: 'plugins.manage' },
      { id: 'settings', label: 'Settings', icon: Settings, key: '9' },
    ],
  },
]

const PLUGIN_ROUTE = /^plugin\/[a-z0-9][a-z0-9.-]{1,62}[a-z0-9]\/[a-z0-9][a-z0-9_-]{0,47}$/u

export function isPluginRoute(route: string): route is PluginRoute {
  return PLUGIN_ROUTE.test(route)
}

export function visibleNavigation(capabilities: readonly string[]): NavigationSection[] {
  return navigation
    .map((section) => ({ ...section, items: section.items.filter((item) => !item.capability || capabilities.includes(item.capability)) }))
    .filter((section) => section.items.length > 0)
}

/** The route a hash names, or overview for anything unknown. Plugin routes are
 * accepted by shape; the page view says so when nobody registered it. */
export function routeFromHash(hash: string): AppRoute {
  const value = hash.replace(/^#\/?/u, '')
  if (navigation.some((section) => section.items.some((item) => item.id === value))) return value as StaticRoute
  return isPluginRoute(value) ? value : 'overview'
}
