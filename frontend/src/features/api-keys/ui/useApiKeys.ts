import { useSyncExternalStore } from 'react'
import { useAppServices } from '../../../app/services'

export function useApiKeys() {
  const { apiKeys } = useAppServices()
  return {
    model: apiKeys,
    state: useSyncExternalStore(apiKeys.subscribe, apiKeys.snapshot),
  }
}
