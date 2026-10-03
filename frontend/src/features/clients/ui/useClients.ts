import { useSyncExternalStore } from 'react'
import { useAppServices } from '../../../app/services'

export function useClients() {
  const { clients } = useAppServices()
  return { model: clients, state: useSyncExternalStore(clients.subscribe, clients.snapshot) }
}
