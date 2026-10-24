import { useSyncExternalStore } from 'react'

const LIGHT_QUERY = '(prefers-color-scheme: light)'

function subscribe(listener: () => void): () => void {
  const query = typeof window.matchMedia === 'function' ? window.matchMedia(LIGHT_QUERY) : null
  query?.addEventListener('change', listener)
  return () => query?.removeEventListener('change', listener)
}

const read = () => (typeof window.matchMedia === 'function' ? window.matchMedia(LIGHT_QUERY).matches : false)

/** The operating system's colour scheme, live: what "System" resolves to. */
export function usePrefersLight(): boolean {
  return useSyncExternalStore(subscribe, read, () => false)
}
