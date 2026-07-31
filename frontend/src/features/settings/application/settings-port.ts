import type { Settings, SettingsUpdateResult } from '../domain/settings'

export interface SettingsPort {
  get(signal?: AbortSignal): Promise<Settings>
  update(settings: Settings, signal?: AbortSignal): Promise<SettingsUpdateResult>
  subscribe(listener: (result: SettingsUpdateResult) => void): () => void
}
