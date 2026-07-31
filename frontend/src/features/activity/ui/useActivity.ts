import { useSyncExternalStore } from 'react'
import { useAppServices } from '../../../app/services'

export function useActivity() {
  const { activity } = useAppServices()
  return useSyncExternalStore(activity.subscribe, activity.snapshot)
}
