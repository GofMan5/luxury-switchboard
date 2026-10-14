import type { AddApiKey, ApiKey, ImportApiKeys, ImportApiKeysReport, UpdateApiKey } from '../domain/api-key'

/** What a pool check found: counts, not names — the rows carry their own badges. */
export interface PoolCheckReport {
  /** Eligible user keys at the start of the round; pinned keys are excluded. */
  readonly total: number
  /** Keys actually dispatched, not the pool size. */
  readonly checked: number
  /** Eligible keys not dispatched before the internal deadline or removal. */
  readonly skipped: number
  readonly accepted: number
  readonly answered: number
  readonly inconclusive: number
  readonly rejected: number
  readonly reachable: boolean
}

export interface ApiKeysListResult {
  readonly keys: readonly ApiKey[]
  /** Full pool size; older backends may return a bounded visible list. */
  readonly available: number
}

export interface ApiKeysPort {
  list(providerId: string, signal?: AbortSignal): Promise<ApiKeysListResult>
  add(value: AddApiKey, signal?: AbortSignal): Promise<ApiKey>
  addMany(value: ImportApiKeys, signal?: AbortSignal): Promise<ImportApiKeysReport>
  update(value: UpdateApiKey, signal?: AbortSignal): Promise<ApiKey>
  remove(providerId: string, keyId: string, signal?: AbortSignal): Promise<void>
  move(providerId: string, keyId: string, direction: -1 | 1, signal?: AbortSignal): Promise<void>
  reset(providerId: string, keyId: string, signal?: AbortSignal): Promise<void>
  checkPool(providerId: string, signal?: AbortSignal): Promise<PoolCheckReport>
  subscribe(listener: (providerId: string) => void): () => void
}
