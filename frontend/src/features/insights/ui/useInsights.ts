import { useSyncExternalStore } from 'react'
import { useAppServices } from '../../../app/services'

export function useInsights() {
  const { insights } = useAppServices()
  return { model: insights, state: useSyncExternalStore(insights.subscribe, insights.snapshot) }
}
