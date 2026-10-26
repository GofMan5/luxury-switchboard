import { describe, expect, it } from 'vitest'
import { elapsedMs, latencyPhases, liveFirst, requestKind, type ActivityRequest } from './activity'

const row = (id: string, state: ActivityRequest['state'], startedAt: string): ActivityRequest => ({
  id, startedAt, updatedAt: startedAt, state, model: 'model', providerId: 'p', providerName: 'P', method: 'POST',
  path: '/v1/responses', queueMs: 0, latencyMs: 0, retries: 0, bytesIn: 0, bytesOut: 0, inputTokens: 0,
  outputTokens: 0, cachedTokens: 0, reasoningTokens: 0, totalTokens: 0, contextTokens: 0, generationMs: 0, tokensPerSecond: 0,
})

describe('liveFirst', () => {
  it('puts every request in flight on top, newest started first, and keeps the finished order', () => {
    const ordered = liveFirst([
      row('done-new', 'completed', '2026-01-01T00:00:09Z'),
      row('old-live', 'active', '2026-01-01T00:00:01Z'),
      row('failed', 'failed', '2026-01-01T00:00:08Z'),
      row('retrying', 'retrying', '2026-01-01T00:00:03Z'),
      row('new-live', 'active', '2026-01-01T00:00:05Z'),
      row('done-old', 'cancelled', '2026-01-01T00:00:00Z'),
    ])
    expect(ordered.map((request) => request.id)).toEqual(['new-live', 'retrying', 'old-live', 'done-new', 'failed', 'done-old'])
  })

  it('does not reorder live rows by their latest event', () => {
    const first = row('first', 'active', '2026-01-01T00:00:02Z')
    const second = row('second', 'active', '2026-01-01T00:00:01Z')
    // `second` just reported a chunk and arrives at the head of the list.
    expect(liveFirst([{ ...second, updatedAt: '2026-01-01T00:00:09Z' }, first]).map((request) => request.id)).toEqual(['first', 'second'])
  })
})

describe('elapsedMs', () => {
  it('counts from the start and never runs behind the sidecar latency', () => {
    const started = Date.parse('2026-01-01T00:00:00Z')
    expect(elapsedMs({ startedAt: '2026-01-01T00:00:00Z', latencyMs: 0 }, started + 4_200)).toBe(4_200)
    expect(elapsedMs({ startedAt: '2026-01-01T00:00:00Z', latencyMs: 9_000 }, started + 4_200)).toBe(9_000)
    expect(elapsedMs({ startedAt: '2026-01-01T00:00:00Z', latencyMs: 0 }, started - 500)).toBe(0)
    expect(elapsedMs({ startedAt: '', latencyMs: 1_500 }, started)).toBe(1_500)
  })
})

describe('requestKind', () => {
  const kind = (path: string, tokens = 0) => requestKind({ path, inputTokens: tokens, outputTokens: 0, totalTokens: 0 })

  it('tells a token count, a model call and a service request apart', () => {
    expect(kind('/v1/messages/count_tokens')).toBe('count')
    expect(kind('/V1/Messages/Count_Tokens/')).toBe('count')
    expect(kind('/v1/messages')).toBe('generation')
    expect(kind('/v1/chat/completions?stream=true')).toBe('generation')
    expect(kind('/responses/')).toBe('generation')
    expect(kind('/api/hello')).toBe('service')
    expect(kind('/v1/models')).toBe('service')
    // Usage proves a model call on a path the relay does not name.
    expect(kind('/v1/custom', 12)).toBe('generation')
  })
})

describe('latencyPhases', () => {
  it('splits the wall-clock into queue, provider wait and streaming', () => {
    expect(latencyPhases({ queueMs: 200, latencyMs: 3_000, ttftMs: 700 })).toEqual({ queue: 200, provider: 500, streaming: 2_300 })
    // Nothing streamed: the provider held the whole answer.
    expect(latencyPhases({ queueMs: 0, latencyMs: 900 })).toEqual({ queue: 0, provider: 900, streaming: 0 })
    // Clock skew never yields a negative or oversized phase.
    expect(latencyPhases({ queueMs: 5_000, latencyMs: 1_000, ttftMs: 400 })).toEqual({ queue: 1_000, provider: 0, streaming: 0 })
    expect(latencyPhases({ queueMs: 100, latencyMs: 1_000, ttftMs: 9_000 })).toEqual({ queue: 100, provider: 900, streaming: 0 })
  })
})
