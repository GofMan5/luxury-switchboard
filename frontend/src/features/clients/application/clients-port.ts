import type { TunnelClient, TunnelClientEvent, TunnelClientProfile } from '../domain/client'

export interface ClientsList {
  readonly clients: readonly TunnelClient[]
  readonly available: number
}

export interface ClientsPort {
  list(signal?: AbortSignal): Promise<ClientsList>
  events(ip: string, signal?: AbortSignal): Promise<readonly TunnelClientEvent[]>
  saveProfile(profile: TunnelClientProfile, signal?: AbortSignal): Promise<ClientsList>
  subscribe(listener: () => void): () => void
}
