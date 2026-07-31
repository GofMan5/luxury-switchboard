import type { ControlPlaneSession } from './session'

export async function createControlPlaneSession(): Promise<ControlPlaneSession> {
  if (!('__TAURI_INTERNALS__' in window)) throw new Error('Switchboard requires the Tauri desktop runtime')
  const { TauriSidecarSession } = await import('./tauri-sidecar-session')
  return new TauriSidecarSession()
}
