import { useSyncExternalStore } from 'react'
import { useAppServices } from '../../../app/services'

export function useModels() {
  const { models } = useAppServices()
  return { model: models, state: useSyncExternalStore(models.subscribe, models.snapshot) }
}
