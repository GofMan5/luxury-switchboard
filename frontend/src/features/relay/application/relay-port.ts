import type { RelaySnapshot } from '../domain/relay'

export interface RelayPort {
  status(signal?: AbortSignal): Promise<RelaySnapshot>
  start(signal?: AbortSignal): Promise<RelaySnapshot>
  stop(signal?: AbortSignal): Promise<RelaySnapshot>
  subscribe(listener: (snapshot: RelaySnapshot) => void): () => void
}
