import type { ComponentType } from 'react'
import {
  Activity,
  CircleGauge,
  Coins,
  Database,
  KeyRound,
  Network,
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
  | 'guardrails'
  | 'tunnel'
  | 'clients'
  | 'insights'
  | 'shared'
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
    ],
  },
  {
    label: 'Safety',
    items: [
      // Both editions inspect provider answers. A public user has the same right
      // to know what a provider sent them as the owner does.
      { id: 'guardrails', label: 'Guardrails', icon: ShieldAlert },
    ],
  },
  ...(__OWNER_EDITION__
    ? ([
        {
          label: 'Publishing',
          // The literal is tested here rather than the re-export, so the public
          // bundle drops their icons too.
          items: [
            { id: 'tunnel', label: 'Tunnel', icon: RadioTower },
            { id: 'clients', label: 'Clients', icon: UsersRound },
            { id: 'shared', label: 'Shared Control', icon: Network },
          ],
        },
      ] as const satisfies readonly NavigationSection[])
    : []),
  {
    label: '',
    items: [{ id: 'settings', label: 'Settings', icon: Settings }],
  },
]
