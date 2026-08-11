import type { ControlPlaneSession } from '../../../platform/stdio/session'
import type { ClientsPort } from '../application/clients-port'
import type { TunnelClient, TunnelClientEvent, TunnelClientProfile } from '../domain/client'

export class StdioClientsPort implements ClientsPort {
  readonly #session: ControlPlaneSession

  constructor(session: ControlPlaneSession) {
    this.#session = session
  }

  async list(signal?: AbortSignal) {
    const value = await this.#session.call<{ clients: readonly TunnelClient[] }>('clients.list', undefined, signal)
    return value.clients
  }

  async events(ip: string, signal?: AbortSignal) {
    const value = await this.#session.call<{ events: readonly TunnelClientEvent[] }>('clients.events', { ip }, signal)
    return value.events
  }

  async saveProfile(profile: TunnelClientProfile, signal?: AbortSignal) {
    const value = await this.#session.call<{ clients: readonly TunnelClient[] }>('clients.profile', profile, signal)
    return value.clients
  }

  subscribe(listener: () => void) {
    return this.#session.subscribe('clients.changed', listener)
  }
}
