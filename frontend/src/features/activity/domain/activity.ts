export type ActivityState = 'active' | 'retrying' | 'completed' | 'failed' | 'cancelled'

export interface ActivityRequest {
  readonly id: string
  readonly startedAt: string
  readonly updatedAt: string
  readonly state: ActivityState
  readonly model: string
  readonly providerId: string
  readonly providerName: string
  readonly method: string
  readonly path: string
  readonly status?: number
  readonly queueMs: number
  readonly latencyMs: number
  readonly retries: number
  readonly bytesIn: number
  readonly bytesOut: number
  readonly errorCode?: string
  readonly errorDetail?: string
  readonly inputTokens: number
  readonly outputTokens: number
  readonly cachedTokens: number
  readonly reasoningTokens: number
  readonly totalTokens: number
  readonly contextTokens: number
  readonly generationMs: number
  readonly tokensPerSecond: number
  // Optional: older sidecars do not send them. The level is what the client
  // asked for; `thinking` is the provider's stream being in its reasoning
  // phase right now, and `thinkingMs` the reasoning spans already closed.
  readonly reasoningEffort?: string
  readonly reasoningBudget?: number
  readonly thinking?: boolean
  readonly thinkingMs?: number
}

/** Still in flight: the relay holds a socket for it. */
export function isLive(request: Pick<ActivityRequest, 'state'>): boolean {
  return request.state === 'active' || request.state === 'retrying'
}

function startedMs(request: ActivityRequest): number {
  return Date.parse(request.startedAt) || 0
}

/** The order the operator reads: every request in flight on top, newest
 * started first so a live row holds its place while its events arrive, then the
 * finished ones in the order they finished. */
export function liveFirst(requests: readonly ActivityRequest[]): ActivityRequest[] {
  const live = requests.filter(isLive).sort((a, b) => startedMs(b) - startedMs(a))
  return [...live, ...requests.filter((request) => !isLive(request))]
}

/** How long a request has been running at `now`, in milliseconds. The
 * sidecar's own latency is a floor, so a clock that disagrees with the
 * sidecar's never makes a running request look younger than it is. */
export function elapsedMs(request: Pick<ActivityRequest, 'startedAt' | 'latencyMs'>, now: number): number {
  const started = Date.parse(request.startedAt)
  const sinceStart = Number.isFinite(started) && started > 0 ? now - started : 0
  return Math.max(sinceStart, request.latencyMs, 0)
}

export interface ActivitySummary {
  readonly requests: number
  readonly active: number
  readonly queued: number
  readonly successRate: number
  readonly p95Ms: number
  readonly rpm: number
}
