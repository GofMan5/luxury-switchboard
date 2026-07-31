import { useSyncExternalStore } from 'react'
import { useAppServices } from '../../../app/services'

export function useStatistics() {
  const { statistics } = useAppServices()
  return { model: statistics, state: useSyncExternalStore(statistics.subscribe, statistics.snapshot) }
}
