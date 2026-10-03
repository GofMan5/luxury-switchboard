import type { ControlPlaneSession } from '../../../platform/stdio/session'
import type { UpdateCheck } from '../domain/update'

export interface UpdatesPort {
  check(): Promise<UpdateCheck>
}

export class StdioUpdatesPort implements UpdatesPort {
  readonly #session: ControlPlaneSession
  constructor(session: ControlPlaneSession) { this.#session = session }
  check(): Promise<UpdateCheck> {
    return this.#session.call<UpdateCheck>('updates.check')
  }
}
