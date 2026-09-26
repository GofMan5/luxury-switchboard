import type { Provider, ProviderCatalog, ProviderHealth, ProviderInput } from '../domain/provider'

export interface ProvidersPort {
  list(signal?: AbortSignal): Promise<ProviderCatalog>
  health(signal?: AbortSignal): Promise<readonly ProviderHealth[]>
  activate(id: string, signal?: AbortSignal): Promise<Provider>
  add(value: ProviderInput, signal?: AbortSignal): Promise<Provider>
  update(id: string, value: ProviderInput, signal?: AbortSignal): Promise<Provider>
  delete(id: string, signal?: AbortSignal): Promise<void>
  subscribe(listener: () => void): () => void
  subscribeHealth(listener: (states: readonly ProviderHealth[]) => void): () => void
}
