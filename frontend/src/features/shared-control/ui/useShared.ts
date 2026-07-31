import { useSyncExternalStore } from 'react'
import { useAppServices } from '../../../app/services'

export function useShared() {
  const { shared } = useAppServices()
  return { model: shared, state: useSyncExternalStore(shared.subscribe, shared.snapshot) }
}
