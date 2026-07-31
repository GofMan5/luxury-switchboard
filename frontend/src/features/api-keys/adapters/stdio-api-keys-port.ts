import type { ControlPlaneSession } from '../../../platform/stdio/session'
import type { AddApiKey, ApiKey, UpdateApiKey } from '../domain/api-key'
import type { ApiKeysPort } from '../application/api-keys-port'

export class StdioApiKeysPort implements ApiKeysPort {
  readonly #session: ControlPlaneSession

  constructor(session: ControlPlaneSession) {
    this.#session = session
  }

  async list(providerId: string, signal?: AbortSignal): Promise<readonly ApiKey[]> {
    const result = await this.#session.call<{ keys: readonly ApiKey[] }>('keys.list', { providerId }, signal)
    return result.keys
  }

  add(value: AddApiKey, signal?: AbortSignal): Promise<ApiKey> {
    return this.#session.call('keys.add', value, signal)
  }

  update(value: UpdateApiKey, signal?: AbortSignal): Promise<ApiKey> {
    return this.#session.call('keys.update', value, signal)
  }

  async remove(providerId: string, keyId: string, signal?: AbortSignal): Promise<void> {
    await this.#session.call('keys.remove', { providerId, keyId }, signal)
  }

  async move(providerId: string, keyId: string, direction: -1 | 1, signal?: AbortSignal): Promise<void> {
    await this.#session.call('keys.move', { providerId, keyId, direction }, signal)
  }

  async reset(providerId: string, keyId: string, signal?: AbortSignal): Promise<void> {
    await this.#session.call('keys.reset', { providerId, keyId }, signal)
  }

  subscribe(listener: (providerId: string) => void): () => void {
    return this.#session.subscribe<{ providerId: string }>('keys.changed', (event) => {
      if (event.payload?.providerId) listener(event.payload.providerId)
    })
  }
}
