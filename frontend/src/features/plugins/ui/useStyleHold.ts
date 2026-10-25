import { useContext, useEffect } from 'react'
import { ServicesContext } from '../../../app/services'

/**
 * Withholds every plugin stylesheet while the calling screen is mounted: a
 * permission review, a plugin's question or its settings must look exactly
 * as the shell draws them, so no stylesheet can hide a warning or relabel a
 * button. Outside the app (a lone dialog in a test) there is nothing to hold.
 */
export function useStyleHold(active = true): void {
  const extensions = useContext(ServicesContext)?.extensions
  useEffect(() => (active && extensions ? extensions.holdStyles() : undefined), [active, extensions])
}
