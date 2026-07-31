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
}

export interface SettingsUpdateResult {
  readonly settings: Settings
  readonly restartRequired: boolean
}
