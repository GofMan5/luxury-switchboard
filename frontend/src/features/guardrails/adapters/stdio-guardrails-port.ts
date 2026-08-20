import type { ControlPlaneSession } from '../../../platform/stdio/session'
import type { GuardrailsPort } from '../application/guardrails-port'
import type { GuardrailMode, GuardrailRecord, GuardrailStatus } from '../domain/guardrail'
import type { Settings } from '../../settings/domain/settings'

export class StdioGuardrailsPort implements GuardrailsPort {
  readonly #session: ControlPlaneSession
  constructor(session: ControlPlaneSession) { this.#session = session }

  status(signal?: AbortSignal): Promise<GuardrailStatus> {
    return this.#session.call('guardrails.status', undefined, signal)
  }

  async findings(limit: number, signal?: AbortSignal) {
    const value = await this.#session.call<{ findings: readonly GuardrailRecord[] | null }>('guardrails.findings', { limit }, signal)
    return value.findings ?? []
  }

  async clear(signal?: AbortSignal) {
    await this.#session.call('guardrails.clear', undefined, signal)
  }

  // The mode is a persisted setting, so it is written through the settings command
  // rather than a second source of truth the two pages could disagree about.
  async setMode(mode: GuardrailMode, signal?: AbortSignal) {
    const current = await this.#session.call<Settings>('settings.get', undefined, signal)
    const result = await this.#session.call<{ settings: Settings }>('settings.update', { ...current, guardrailMode: mode }, signal)
    return result.settings.guardrailMode
  }

  subscribe(listener: (record: GuardrailRecord) => void) {
    return this.#session.subscribe<GuardrailRecord>('guardrails.finding', (event) => { if (event.payload) listener(event.payload) })
  }

  subscribeStatus(listener: () => void) {
    return this.#session.subscribe('guardrails.changed', () => listener())
  }
}
