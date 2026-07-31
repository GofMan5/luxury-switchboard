import type { ControlPlaneSession } from '../../../platform/stdio/session'
import type { ProvidersPort } from '../application/providers-port'
import type { Provider, ProviderCatalog, ProviderInput } from '../domain/provider'

export class StdioProvidersPort implements ProvidersPort {
  readonly #session: ControlPlaneSession

  constructor(session: ControlPlaneSession) {
    this.#session = session
  }

  list(signal?: AbortSignal): Promise<ProviderCatalog> {
    return this.#session.call('providers.list', undefined, signal)
  }

  activate(id: string, signal?: AbortSignal): Promise<Provider> {
    return this.#session.call('providers.activate', { id }, signal)
  }

  add(value: ProviderInput, signal?: AbortSignal): Promise<Provider> {
    return this.#session.call('providers.add', value, signal)
  }

  update(id: string, value: ProviderInput, signal?: AbortSignal): Promise<Provider> {
    return this.#session.call('providers.update', { id, ...value }, signal)
  }

  async delete(id: string, signal?: AbortSignal): Promise<void> {
    await this.#session.call('providers.delete', { id }, signal)
  }

  subscribe(listener: () => void): () => void {
    return this.#session.subscribe('providers.changed', listener)
  }
}
