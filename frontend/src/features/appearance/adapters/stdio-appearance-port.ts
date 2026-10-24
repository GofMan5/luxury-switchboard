import type { ControlPlaneSession } from '../../../platform/stdio/session'
import type { AppearancePort } from '../application/appearance-port'
import { normalizeAppearance, type Appearance } from '../domain/appearance'

export class StdioAppearancePort implements AppearancePort {
  readonly durable = true
  readonly #session: ControlPlaneSession

  constructor(session: ControlPlaneSession) { this.#session = session }

  async get(signal?: AbortSignal): Promise<Appearance> {
    return normalizeAppearance(await this.#session.call('appearance.get', undefined, signal))
  }

  async update(appearance: Appearance, signal?: AbortSignal): Promise<Appearance> {
    return normalizeAppearance(await this.#session.call('appearance.update', appearance, signal))
  }

  subscribe(listener: (appearance: Appearance) => void): () => void {
    return this.#session.subscribe('appearance.changed', (event) => {
      if (event.payload) listener(normalizeAppearance(event.payload))
    })
  }
}
