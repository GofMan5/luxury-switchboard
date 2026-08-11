import type { TunnelClient, TunnelClientEvent, TunnelClientProfile } from '../domain/client'

export interface ClientsPort {
  list(signal?: AbortSignal): Promise<readonly TunnelClient[]>
  events(ip: string, signal?: AbortSignal): Promise<readonly TunnelClientEvent[]>
  saveProfile(profile: TunnelClientProfile, signal?: AbortSignal): Promise<readonly TunnelClient[]>
  subscribe(listener: () => void): () => void
}
