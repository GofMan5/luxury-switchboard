import type { SharedAction, SharedSnapshot } from '../domain/snapshot'

export interface SharedPort {
  list(signal?: AbortSignal): Promise<SharedSnapshot>
  control(position: number, revision: number, action: SharedAction, signal?: AbortSignal): Promise<SharedSnapshot>
}
