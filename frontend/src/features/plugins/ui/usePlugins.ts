import { useSyncExternalStore } from 'react'
import { useAppServices } from '../../../app/services'

export function usePlugins() {
  const { plugins } = useAppServices()
  return { model: plugins, state: useSyncExternalStore(plugins.subscribe, plugins.snapshot) }
}
