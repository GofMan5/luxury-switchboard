import { useSyncExternalStore } from 'react'
import { useAppServices } from '../../../app/services'

export function useSettings() {
  const { settings } = useAppServices()
  return { model: settings, state: useSyncExternalStore(settings.subscribe, settings.snapshot) }
}
