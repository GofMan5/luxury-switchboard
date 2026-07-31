import { useSyncExternalStore } from 'react'
import { useAppServices } from '../../../app/services'

export function useProviders() {
  const { providers } = useAppServices()
  return {
    model: providers,
    state: useSyncExternalStore(providers.subscribe, providers.snapshot),
  }
}
