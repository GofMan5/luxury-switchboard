import type { GuardrailMode } from '../../guardrails/domain/guardrail'

export interface Settings {
  readonly listenerPort: number
  readonly maxRequestMiB: number
  readonly headerTimeoutSeconds: number
  readonly streamIdleSeconds: number
  readonly retryBaseMilliseconds: number
  readonly retryMaxSeconds: number
  readonly permanentAttempts: number
  readonly maxQueued: number
  readonly activityCapacity: number
  readonly historyRetentionDays: number
  readonly tunnelRetentionHours: number
  /**
   * Guardrail settings are carried here even though they are edited on the
   * Guardrails page. The control plane replaces the whole record on save, so a
   * field missing from this type would be silently reset by any other change.
   */
  readonly guardrailMode: GuardrailMode
  readonly guardrailFindings: number
}

export interface SettingsUpdateResult {
  readonly settings: Settings
  readonly restartRequired: boolean
}
