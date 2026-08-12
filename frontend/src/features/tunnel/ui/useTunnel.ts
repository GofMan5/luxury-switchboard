import { useSyncExternalStore } from 'react'
import { useAppServices } from '../../../app/services'

export function useTunnel() {
  const { tunnel } = useAppServices()
  // Reached only from the owner edition, where the model always exists.
  if (!tunnel) throw new Error('Tunnel is not available in this edition')
  return { model: tunnel, state: useSyncExternalStore(tunnel.subscribe, tunnel.snapshot) }
}
