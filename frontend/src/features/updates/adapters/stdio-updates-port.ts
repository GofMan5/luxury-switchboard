import type { ControlPlaneSession } from '../../../platform/stdio/session'
import type { UpdateCheck, UpdateInstall, UpdateInstallProgress } from '../domain/update'
import type { UpdatesPort } from '../application/updates-port'

/** Reads and installs updates through the framed stdio control plane. */
export class StdioUpdatesPort implements UpdatesPort {
  readonly #session: ControlPlaneSession

  constructor(session: ControlPlaneSession) {
    this.#session = session
  }

  check(signal?: AbortSignal): Promise<UpdateCheck | null> {
    return this.#session.call<UpdateCheck>('updates.check', {}, signal)
  }

  status(signal?: AbortSignal): Promise<UpdateCheck | null> {
    return this.#session.call<UpdateCheck>('updates.status', {}, signal)
  }

  install(signal?: AbortSignal): Promise<UpdateInstall> {
    return this.#session.call<UpdateInstall>('updates.install', {}, signal)
  }

  onInstallProgress(listener: (report: UpdateInstallProgress) => void): () => void {
    return this.#session.subscribe('updates.installProgress', (frame) => listener(frame.payload as UpdateInstallProgress))
  }

  onStateChanged(listener: (check: UpdateCheck) => void): () => void {
    return this.#session.subscribe('updates.stateChanged', (frame) => listener(frame.payload as UpdateCheck))
  }
}
