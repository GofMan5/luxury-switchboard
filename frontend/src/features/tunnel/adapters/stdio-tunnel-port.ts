import type { ControlPlaneSession } from '../../../platform/stdio/session'
import type { TunnelPort } from '../application/tunnel-port'
import type { PrivacyReport } from '../domain/privacy'
import type { TunnelConfig, TunnelSnapshot } from '../domain/tunnel'

export class StdioTunnelPort implements TunnelPort {
  readonly #session: ControlPlaneSession
  constructor(session: ControlPlaneSession) { this.#session = session }
  get(signal?: AbortSignal) { return this.#session.call<TunnelSnapshot>('tunnel.get', undefined, signal) }
  configure(config: TunnelConfig, signal?: AbortSignal) { return this.#session.call<TunnelSnapshot>('tunnel.configure', config, signal) }
  start(signal?: AbortSignal) { return this.#session.call<TunnelSnapshot>('tunnel.start', undefined, signal) }
  stop(signal?: AbortSignal) { return this.#session.call<TunnelSnapshot>('tunnel.stop', undefined, signal) }
  async rotate(signal?: AbortSignal) { const result = await this.#session.call<{ token: string }>('tunnel.rotate', undefined, signal); return result.token }
  async reveal(signal?: AbortSignal) { const result = await this.#session.call<{ token: string }>('tunnel.reveal', undefined, signal); return result.token }
  privacyTest(signal?: AbortSignal) { return this.#session.call<PrivacyReport>('tunnel.privacy_test', undefined, signal) }
  subscribe(listener: (snapshot: TunnelSnapshot) => void) { return this.#session.subscribe<TunnelSnapshot>('tunnel.changed', (event) => { if (event.payload) listener(event.payload) }) }
}
