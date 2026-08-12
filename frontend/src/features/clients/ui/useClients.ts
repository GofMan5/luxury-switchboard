import { useSyncExternalStore } from 'react'
import { useAppServices } from '../../../app/services'

export function useClients() {
  const { clients } = useAppServices()
  // Reached only from the owner edition, where the model always exists.
  if (!clients) throw new Error('Tunnel clients are not available in this edition')
  return { model: clients, state: useSyncExternalStore(clients.subscribe, clients.snapshot) }
}
