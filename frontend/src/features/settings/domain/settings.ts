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
  /** Per-provider inspection overrides: monitor or block for providers that
   * earned distrust, regardless of the global mode. An older control plane
   * omits the field; it reads as no overrides. */
  readonly guardrailProviderModes: Readonly<Record<string, GuardrailMode | ''>>
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
  /** Wire compat: the control plane applies every setting live and answers
   * false. The field stays on the shape so an older sidecar still parses. */
  readonly restartRequired: boolean
}

/**
 * The defaults the control plane ships, mirrored from the backend's
 * domain.Defaults(). The page shows them next to each field and offers a
 * reset, so they must read the same values — one place here, one place there,
 * and the field notes name the numbers.
 */
export const SETTINGS_DEFAULTS: Settings = {
  listenerPort: 8798,
  maxRequestMiB: 64,
  headerTimeoutSeconds: 45,
  streamIdleSeconds: 60,
  retryBaseMilliseconds: 500,
  retryMaxSeconds: 30,
  permanentAttempts: 2,
  maxQueued: 10_000,
  activityCapacity: 2_000,
  historyRetentionDays: 30,
  tunnelRetentionHours: 72,
  guardrailMode: 'monitor',
  guardrailProviderModes: {},
  guardrailFindings: 500,
  notificationsEnabled: true,
  providerHealthEnabled: true,
  animationsEnabled: true,
  failoverEnabled: true,
  chainMode: 'balance',
}
