import { useSyncExternalStore } from 'react'
import { useAppServices } from '../../../app/services'

export function useRelay() {
  const { relay } = useAppServices()
  return {
    model: relay,
    state: useSyncExternalStore(relay.subscribe, relay.snapshot),
  }
}
