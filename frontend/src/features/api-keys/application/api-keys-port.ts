import type { AddApiKey, ApiKey, ImportApiKeys, ImportApiKeysReport, UpdateApiKey } from '../domain/api-key'

/** What a pool check found: counts, not names — the rows carry their own badges. */
export interface PoolCheckReport {
  readonly checked: number
  readonly rejected: number
  readonly reachable: boolean
}

export interface ApiKeysPort {
  list(providerId: string, signal?: AbortSignal): Promise<readonly ApiKey[]>
  add(value: AddApiKey, signal?: AbortSignal): Promise<ApiKey>
  addMany(value: ImportApiKeys, signal?: AbortSignal): Promise<ImportApiKeysReport>
  update(value: UpdateApiKey, signal?: AbortSignal): Promise<ApiKey>
  remove(providerId: string, keyId: string, signal?: AbortSignal): Promise<void>
  move(providerId: string, keyId: string, direction: -1 | 1, signal?: AbortSignal): Promise<void>
  reset(providerId: string, keyId: string, signal?: AbortSignal): Promise<void>
  checkPool(providerId: string, signal?: AbortSignal): Promise<PoolCheckReport>
  subscribe(listener: (providerId: string) => void): () => void
}
