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
  UsersRound,
} from 'lucide-react'
import { OWNER_EDITION } from './edition'

export type AppRoute =
  | 'overview'
  | 'activity'
  | 'providers'
  | 'keys'
  | 'routes'
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
  // Publishing workspaces exist only in the owner edition.
  ...(OWNER_EDITION
    ? ([
        { id: 'tunnel', label: 'Tunnel', icon: RadioTower },
        { id: 'clients', label: 'Clients', icon: UsersRound },
      ] as const satisfies readonly NavigationItem[])
    : []),
  { id: 'statistics', label: 'Statistics', icon: ChartNoAxesCombined },
  ...(OWNER_EDITION
    ? ([{ id: 'shared', label: 'Shared Control', icon: Network }] as const satisfies readonly NavigationItem[])
    : []),
  { id: 'settings', label: 'Settings', icon: Settings },
]
