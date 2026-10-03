import type { ComponentType } from 'react'
import {
  Activity,
  CircleGauge,
  Coins,
  Database,
  FlaskConical,
  KeyRound,
  RadioTower,
  Route,
  Settings,
  ShieldAlert,
  UsersRound,
} from 'lucide-react'

export type AppRoute =
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
  | 'settings'

export interface NavigationItem {
  readonly id: AppRoute
  readonly label: string
  readonly icon: ComponentType<{ size?: number; strokeWidth?: number }>
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
      { id: 'overview', label: 'Overview', icon: CircleGauge },
      { id: 'activity', label: 'Live Activity', icon: Activity },
      { id: 'insights', label: 'Insights', icon: Coins },
    ],
  },
  {
    label: 'Routing',
    items: [
      { id: 'providers', label: 'Providers', icon: Database },
      { id: 'keys', label: 'API Keys', icon: KeyRound },
      { id: 'routes', label: 'Model Routes', icon: Route },
      { id: 'tests', label: 'Tests', icon: FlaskConical },
    ],
  },
  {
    label: 'Safety',
    items: [
      // Everyone inspects provider answers. Every user has the same right
      // to know what a provider sent them as the owner does.
      { id: 'guardrails', label: 'Guardrails', icon: ShieldAlert },
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
    items: [{ id: 'settings', label: 'Settings', icon: Settings }],
  },
]
