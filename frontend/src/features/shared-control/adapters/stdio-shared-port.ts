import type { ControlPlaneSession } from '../../../platform/stdio/session'
import type { SharedPort } from '../application/shared-port'
import type { SharedAction, SharedSnapshot } from '../domain/snapshot'

export class StdioSharedPort implements SharedPort {
  readonly #session: ControlPlaneSession
  constructor(session: ControlPlaneSession) { this.#session = session }
  list(signal?: AbortSignal) { return this.#session.call<SharedSnapshot>('shared.list', undefined, signal) }
  control(position: number, revision: number, action: SharedAction, signal?: AbortSignal) {
    return this.#session.call<SharedSnapshot>('shared.control', { position, revision, action }, signal)
  }
}
