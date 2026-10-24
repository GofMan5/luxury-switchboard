import type { ComponentType } from 'react'
import {
  Activity,
  BarChart3,
  Bell,
  Box,
  Clock3,
  Cpu,
  Database,
  Gauge,
  Globe,
  Layers,
  LayoutDashboard,
  List,
  Puzzle,
  Shield,
  Terminal,
  Zap,
} from 'lucide-react'

type Icon = ComponentType<{ size?: number; strokeWidth?: number }>

/** The icon names a plugin page may ask for. A closed set keeps the bundle
 * from shipping every icon for a handful of plugin pages; anything else gets
 * the neutral puzzle piece. Documented in docs/PLUGINS.md. */
export const PLUGIN_ICONS: Readonly<Record<string, Icon>> = {
  activity: Activity,
  chart: BarChart3,
  bell: Bell,
  box: Box,
  clock: Clock3,
  cpu: Cpu,
  database: Database,
  gauge: Gauge,
  globe: Globe,
  layers: Layers,
  dashboard: LayoutDashboard,
  list: List,
  puzzle: Puzzle,
  shield: Shield,
  terminal: Terminal,
  zap: Zap,
}

export function pluginIcon(name: string | undefined): Icon {
  return (name && PLUGIN_ICONS[name]) || Puzzle
}
