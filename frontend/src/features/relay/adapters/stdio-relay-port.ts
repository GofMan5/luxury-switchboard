import type { ControlPlaneSession } from '../../../platform/stdio/session'
import type { RelayPort } from '../application/relay-port'
import type { RelaySnapshot } from '../domain/relay'

export class StdioRelayPort implements RelayPort {
  readonly #session: ControlPlaneSession

  constructor(session: ControlPlaneSession) {
    this.#session = session
  }

  status(signal?: AbortSignal): Promise<RelaySnapshot> {
    return this.#session.call('relay.status', undefined, signal)
  }

  start(signal?: AbortSignal): Promise<RelaySnapshot> {
    return this.#session.call('relay.start', undefined, signal)
  }

  stop(signal?: AbortSignal): Promise<RelaySnapshot> {
    return this.#session.call('relay.stop', undefined, signal)
  }

  subscribe(listener: (snapshot: RelaySnapshot) => void): () => void {
    return this.#session.subscribe<RelaySnapshot>('relay.changed', (event) => {
      if (event.payload) listener(event.payload)
    })
  }
}
