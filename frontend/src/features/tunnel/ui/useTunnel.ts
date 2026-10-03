import { useSyncExternalStore } from 'react'
import { useAppServices } from '../../../app/services'

export function useTunnel() {
  const { tunnel } = useAppServices()
  return { model: tunnel, state: useSyncExternalStore(tunnel.subscribe, tunnel.snapshot) }
}
