import type { ControlPlaneSession } from './session'

export async function createControlPlaneSession(): Promise<ControlPlaneSession> {
  // Development-only fixture: `pnpm dev` in a plain browser with ?fixture=1.
  // The literal DEV flag keeps the whole branch out of production bundles.
  if (import.meta.env.DEV && new URLSearchParams(window.location.search).has('fixture')) {
    const { FixtureSession } = await import('../../dev/fixture-session')
    return new FixtureSession()
  }
  if (!('__TAURI_INTERNALS__' in window)) throw new Error('Luxury Switchboard requires the Tauri desktop runtime')
  const { TauriSidecarSession } = await import('./tauri-sidecar-session')
  return new TauriSidecarSession()
}
