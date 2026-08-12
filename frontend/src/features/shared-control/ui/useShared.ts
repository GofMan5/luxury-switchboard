import { useSyncExternalStore } from 'react'
import { useAppServices } from '../../../app/services'

export function useShared() {
  const { shared } = useAppServices()
  // Reached only from the owner edition, where the model always exists.
  if (!shared) throw new Error('Shared control is not available in this edition')
  return { model: shared, state: useSyncExternalStore(shared.subscribe, shared.snapshot) }
}
