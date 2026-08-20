import type { ComponentType } from 'react'
import {
  Activity,
  ChartNoAxesCombined,
  CircleGauge,
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
  | 'statistics'
  | 'shared'
  | 'settings'

export interface NavigationItem {
  readonly id: AppRoute
  readonly label: string
  readonly icon: ComponentType<{ size?: number; strokeWidth?: number }>
}

export const navigation: readonly NavigationItem[] = [
  { id: 'overview', label: 'Overview', icon: CircleGauge },
  { id: 'activity', label: 'Live Activity', icon: Activity },
  { id: 'providers', label: 'Providers', icon: Database },
  { id: 'keys', label: 'API Keys', icon: KeyRound },
  { id: 'routes', label: 'Model Routes', icon: Route },
  // Both editions inspect provider answers. A public user has the same right to
  // know what a provider sent them as the owner does.
  { id: 'guardrails', label: 'Guardrails', icon: ShieldAlert },
  // Publishing workspaces exist only in the owner edition. The literal is tested
  // here rather than the re-export, so the public bundle drops their icons too.
  ...(__OWNER_EDITION__
    ? ([
        { id: 'tunnel', label: 'Tunnel', icon: RadioTower },
        { id: 'clients', label: 'Clients', icon: UsersRound },
      ] as const satisfies readonly NavigationItem[])
    : []),
  { id: 'statistics', label: 'Statistics', icon: ChartNoAxesCombined },
  ...(__OWNER_EDITION__
    ? ([{ id: 'shared', label: 'Shared Control', icon: Network }] as const satisfies readonly NavigationItem[])
    : []),
  { id: 'settings', label: 'Settings', icon: Settings },
]
