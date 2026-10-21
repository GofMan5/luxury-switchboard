export type ModelTestProfile = 'short' | 'full-128k'

export interface ModelTestProfileInfo {
  readonly label: string
  readonly eyebrow: string
  readonly description: string
  readonly detail: string
}

export const MODEL_TEST_PROFILES: Readonly<Record<ModelTestProfile, ModelTestProfileInfo>> = {
  short: {
    label: 'Short',
    eyebrow: 'Fast reachability',
    description: 'A small streaming probe for a quick health check.',
    detail: 'Low cost · measures first token, total time, and decode rate',
  },
  'full-128k': {
    label: 'Full · 128k',
    eyebrow: 'Context capacity',
    description: 'A bounded ~128k-token context probe for long-context routing.',
    detail: 'Higher cost · up to 5 minutes per provider request · cancellable',
  },
}

export interface ModelTestResult {
  readonly runId: string
  readonly providerId: string
  readonly model: string
  readonly state: 'testing' | 'available' | 'unavailable' | 'timeout'
  readonly status: number
  readonly latencyMs: number
  /** Time to the first content token of the streaming probe; zero on a
   * refusal. OutputTokens is the provider's own count. */
  readonly ttftMs?: number
  readonly outputTokens?: number
  /** Decode rate the relay measured: tokens after the first over the window
   * from the first token to the end. Absent when the stream was too short or
   * too bursty to measure — never derived here from the totals. */
  readonly outputTps?: number
  readonly errorCode?: string
}
