import type { PrivacyReport } from '../domain/privacy'
import type { TunnelConfig, TunnelSnapshot } from '../domain/tunnel'

export interface TunnelPort {
  get(signal?: AbortSignal): Promise<TunnelSnapshot>
  configure(config: TunnelConfig, signal?: AbortSignal): Promise<TunnelSnapshot>
  start(signal?: AbortSignal): Promise<TunnelSnapshot>
  stop(signal?: AbortSignal): Promise<TunnelSnapshot>
  rotate(signal?: AbortSignal): Promise<string>
  reveal(signal?: AbortSignal): Promise<string>
  privacyTest(signal?: AbortSignal): Promise<PrivacyReport>
  subscribe(listener: (snapshot: TunnelSnapshot) => void): () => void
}
