import type { Provider, ProviderCatalog, ProviderInput } from '../domain/provider'

export interface ProvidersPort {
  list(signal?: AbortSignal): Promise<ProviderCatalog>
  activate(id: string, signal?: AbortSignal): Promise<Provider>
  add(value: ProviderInput, signal?: AbortSignal): Promise<Provider>
  update(id: string, value: ProviderInput, signal?: AbortSignal): Promise<Provider>
  delete(id: string, signal?: AbortSignal): Promise<void>
  subscribe(listener: () => void): () => void
}
