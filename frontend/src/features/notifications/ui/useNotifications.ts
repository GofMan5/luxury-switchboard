import { useSyncExternalStore } from 'react'
import { useAppServices } from '../../../app/services'

export function useNotifications() {
  const { notifications } = useAppServices()
  return {
    model: notifications,
    state: useSyncExternalStore(notifications.subscribe, notifications.snapshot),
  }
}
