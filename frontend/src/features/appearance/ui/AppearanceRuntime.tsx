import { useEffect, useSyncExternalStore } from 'react'
import { useAppServices } from '../../../app/services'
import { safeMode } from '../../../shared/runtime/safe-mode'
import { applyAppearance } from './apply-appearance'
import { usePrefersLight } from './usePrefersLight'

/** Keeps the document in step with the appearance record, the contributed
 * themes and stylesheets, and the system colour scheme. Renders nothing. */
export function AppearanceRuntime() {
  const { appearance, extensions } = useAppServices()
  const state = useSyncExternalStore(appearance.subscribe, appearance.snapshot)
  const contributions = useSyncExternalStore(extensions.subscribe, extensions.snapshot)
  const light = usePrefersLight()

  useEffect(() => {
    applyAppearance({ appearance: state.appearance, themes: contributions.themes, styles: contributions.styles, prefersLight: light, safe: safeMode })
  }, [state.appearance, contributions.themes, contributions.styles, light])

  return null
}
