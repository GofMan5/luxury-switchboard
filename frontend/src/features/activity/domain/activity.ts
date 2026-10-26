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
  /** Decode rate: tokens after the first over the window that followed it;
   * zero when the answer failed or streamed too little to measure. */
  readonly tokensPerSecond: number
  /** The client's wait for the first generated token, answer or reasoning,
   * from the request's arrival; absent before one arrived. */
  readonly ttftMs?: number
  // Optional: older sidecars do not send them. The level is what the client
  // asked for; `thinking` is the provider's stream being in its reasoning
  // phase right now, and `thinkingMs` the reasoning spans already closed.
  readonly reasoningEffort?: string
  readonly reasoningBudget?: number
  readonly thinking?: boolean
  readonly thinkingMs?: number
  /** Operator labels from request rules and relay hooks, in first-seen order;
   * absent when there are none (and from older sidecars). */
  readonly tags?: readonly string[]
}

/** The sidecar's own bound on one request's labels. */
export const MAX_ACTIVITY_TAGS = 16
const TAG = /^[a-z0-9][a-z0-9_.-]{0,31}$/u

/**
 * The record as the screen may trust it: tags are data from the wire, so only
 * well-formed labels survive, once each, at most {@link MAX_ACTIVITY_TAGS}.
 * An empty or invalid list drops the field instead of rendering nothing.
 */
export function normalizeActivityRequest(request: ActivityRequest): ActivityRequest {
  const raw: unknown = (request as { tags?: unknown }).tags
  if (raw === undefined) return request
  const tags: string[] = []
  if (Array.isArray(raw)) {
    for (const tag of raw) {
      if (tags.length >= MAX_ACTIVITY_TAGS) break
      if (typeof tag === 'string' && TAG.test(tag) && !tags.includes(tag)) tags.push(tag)
    }
  }
  const { tags: _dropped, ...rest } = request
  return tags.length > 0 ? { ...rest, tags } : rest
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

/** What the request asked the provider for: an answer to generate, a prompt
 * to size, or neither (model lists, probes, health checks). */
export type RequestKind = 'generation' | 'count' | 'service'

const GENERATION_PATH = /\/(responses|chat\/completions|completions|messages|embeddings)$/u

export function requestKind(request: Pick<ActivityRequest, 'path' | 'inputTokens' | 'outputTokens' | 'totalTokens'>): RequestKind {
  const path = request.path.toLowerCase().split('?')[0].replace(/\/+$/u, '')
  if (path.endsWith('/messages/count_tokens')) return 'count'
  if (GENERATION_PATH.test(path) || request.inputTokens + request.outputTokens + request.totalTokens > 0) return 'generation'
  return 'service'
}

/** Where the wall-clock went, in milliseconds: the relay's queue, the wait for
 * the provider's first token (or its whole answer when nothing streamed) and
 * the streaming that followed. First token is measured from arrival, so it
 * already contains the queue. */
export function latencyPhases(request: Pick<ActivityRequest, 'queueMs' | 'latencyMs' | 'ttftMs'>): { queue: number; provider: number; streaming: number } {
  const total = Math.max(request.latencyMs, 0)
  const queue = Math.min(Math.max(request.queueMs, 0), total)
  const first = request.ttftMs && request.ttftMs > queue ? Math.min(request.ttftMs, total) : total
  return { queue, provider: first - queue, streaming: total - first }
}
