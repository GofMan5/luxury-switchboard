import { useSyncExternalStore } from 'react'
import { useAppServices } from '../../../app/services'

export function useRoutes() {
  const { routes } = useAppServices()
  return {
    model: routes,
    state: useSyncExternalStore(routes.subscribe, routes.snapshot),
  }
}
