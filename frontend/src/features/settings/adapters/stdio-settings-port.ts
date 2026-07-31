import type { ControlPlaneSession } from '../../../platform/stdio/session'
import type { SettingsPort } from '../application/settings-port'
import type { Settings, SettingsUpdateResult } from '../domain/settings'

export class StdioSettingsPort implements SettingsPort {
  readonly #session: ControlPlaneSession
  constructor(session: ControlPlaneSession) { this.#session = session }
  get(signal?: AbortSignal): Promise<Settings> { return this.#session.call('settings.get', undefined, signal) }
  update(settings: Settings, signal?: AbortSignal): Promise<SettingsUpdateResult> { return this.#session.call('settings.update', settings, signal) }
  subscribe(listener: (result: SettingsUpdateResult) => void): () => void {
    return this.#session.subscribe<SettingsUpdateResult>('settings.changed', (event) => { if (event.payload) listener(event.payload) })
  }
}
