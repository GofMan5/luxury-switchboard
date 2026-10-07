import { useSyncExternalStore } from 'react'
import { useAppServices } from '../../../app/services'

export function useCodex() {
  const { codex } = useAppServices()
  return {
    model: codex,
    state: useSyncExternalStore(codex.subscribe, codex.snapshot),
  }
}
