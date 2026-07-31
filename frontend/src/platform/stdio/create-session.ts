import { BrowserSession } from './browser-session'
import type { ControlPlaneSession } from './session'

export async function createControlPlaneSession(): Promise<ControlPlaneSession> {
  if ('__TAURI_INTERNALS__' in window) {
    const { TauriSidecarSession } = await import('./tauri-sidecar-session')
    return new TauriSidecarSession()
  }
  return new BrowserSession()
}
