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
  /**
   * Master switch for notification toasts and the unread badge. The feed
   * itself stays live either way: history is not the operator's decision to
   * make per event. An older control plane omits these fields; the defaults
   * read as on.
   */
  readonly notificationsEnabled: boolean
  /** Runs the background reachability probe of enabled providers. */
  readonly providerHealthEnabled: boolean
  /** Motion switch for the interface. Reduced-motion at the OS level still wins. */
  readonly animationsEnabled: boolean
  /** Route chains: on moves a request to the next provider after a verdict no
   * retry could change; off keeps routing strict. */
  readonly failoverEnabled: boolean
  /** How a healthy chain shares requests: 'failover' serves strictly by
   * priority, 'balance' round-robins the healthy entries. */
  readonly chainMode: 'failover' | 'balance'
}

export interface SettingsUpdateResult {
  readonly settings: Settings
  readonly restartRequired: boolean
}
