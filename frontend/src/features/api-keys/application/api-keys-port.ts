import type { AddApiKey, ApiKey, UpdateApiKey } from '../domain/api-key'

export interface ApiKeysPort {
  list(providerId: string, signal?: AbortSignal): Promise<readonly ApiKey[]>
  add(value: AddApiKey, signal?: AbortSignal): Promise<ApiKey>
  update(value: UpdateApiKey, signal?: AbortSignal): Promise<ApiKey>
  remove(providerId: string, keyId: string, signal?: AbortSignal): Promise<void>
  move(providerId: string, keyId: string, direction: -1 | 1, signal?: AbortSignal): Promise<void>
  reset(providerId: string, keyId: string, signal?: AbortSignal): Promise<void>
  subscribe(listener: (providerId: string) => void): () => void
}
