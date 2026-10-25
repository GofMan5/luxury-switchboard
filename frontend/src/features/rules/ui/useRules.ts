import { useSyncExternalStore } from 'react'
import { useAppServices } from '../../../app/services'

export function useRules() {
  const { rules } = useAppServices()
  const state = useSyncExternalStore(rules.subscribe, rules.snapshot, rules.snapshot)
  return { state, model: rules }
}
