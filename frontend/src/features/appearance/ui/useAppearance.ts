import { useSyncExternalStore } from 'react'
import { useAppServices } from '../../../app/services'

export function useAppearance() {
  const { appearance } = useAppServices()
  return { model: appearance, state: useSyncExternalStore(appearance.subscribe, appearance.snapshot) }
}
