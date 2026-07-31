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
  { id: 'tunnel', label: 'Tunnel', icon: RadioTower },
  { id: 'clients', label: 'Clients', icon: UsersRound },
  { id: 'statistics', label: 'Statistics', icon: ChartNoAxesCombined },
  { id: 'shared', label: 'Shared Control', icon: Network },
  { id: 'settings', label: 'Settings', icon: Settings },
]
