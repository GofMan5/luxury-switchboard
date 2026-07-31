import type { TunnelClient, TunnelClientEvent } from '../domain/client'

export interface ClientsPort {
  list(signal?: AbortSignal): Promise<readonly TunnelClient[]>
  events(ip: string, signal?: AbortSignal): Promise<readonly TunnelClientEvent[]>
  subscribe(listener: () => void): () => void
}
