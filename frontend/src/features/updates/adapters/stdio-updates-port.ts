import type { ControlPlaneSession } from '../../../platform/stdio/session'
import type { UpdateCheck, UpdateInstall, UpdateInstallProgress } from '../domain/update'

export interface UpdatesPort {
  check(signal?: AbortSignal): Promise<UpdateCheck>
  install(signal?: AbortSignal): Promise<UpdateInstall>
  /** Installs report themselves as they go; this is the subscription. */
  onInstallProgress(listener: (report: UpdateInstallProgress) => void): () => void
}

export class StdioUpdatesPort implements UpdatesPort {
  readonly #session: ControlPlaneSession
  constructor(session: ControlPlaneSession) { this.#session = session }
  check(signal?: AbortSignal): Promise<UpdateCheck> {
    return this.#session.call<UpdateCheck>('updates.check', {}, signal)
  }
  install(signal?: AbortSignal): Promise<UpdateInstall> {
    return this.#session.call<UpdateInstall>('updates.install', {}, signal)
  }
  onInstallProgress(listener: (report: UpdateInstallProgress) => void): () => void {
    return this.#session.subscribe('updates.installProgress', (payload) => {
      listener(payload as unknown as UpdateInstallProgress)
    })
  }
}
