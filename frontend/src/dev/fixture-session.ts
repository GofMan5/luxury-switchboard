/**
 * Development fixture session: answers every control-plane command with
 * realistic local data and emits live activity ticks, so the interface can be
 * exercised in a plain browser via `pnpm dev` + `?fixture=1`. It is reachable
 * only through a dynamic import behind `import.meta.env.DEV`; production builds
 * tree-shake the branch and never bundle it.
 */
import type { EventFrame } from '../shared/contracts/protocol'
import type { ControlPlaneSession, EventListener } from '../platform/stdio/session'
import type { ActivityRequest } from '../features/activity/domain/activity'
import type { Provider } from '../features/providers/domain/provider'
import type { ModelRoute } from '../features/model-routes/domain/route'
import type { Settings } from '../features/settings/domain/settings'
import type { GuardrailRecord } from '../features/guardrails/domain/guardrail'
import type { InsightsReport, HistoryRequest, ModelPrice } from '../features/insights/domain/insights'

const now = Date.now()
const iso = (offsetMs: number): string => new Date(now - offsetMs).toISOString()

// Fixture providers are fictional on purpose: the file is committed, and a
// development fixture is not where real vendor names belong.
const providers: Provider[] = [
  { id: 'north-relay', name: 'North Relay', baseUrl: 'https://api.north-relay.example/v1', authMode: 'bearer', authHeader: 'Authorization', dialect: 'openai', modelsPath: '/v1/models', format: 'chat', chatPath: '/v1/chat/completions', imageCompat: false, rpm: 40, rateUnit: 'minute', cacheTtl: '0s', enabled: true, keyConfigured: true, keyCount: 3, builtin: false },
  { id: 'vendor-hub', name: 'Vendor Hub', baseUrl: 'https://router.vendor-hub.example/v1', authMode: 'bearer', authHeader: 'Authorization', dialect: 'auto', modelsPath: '/v1/models', format: 'responses', chatPath: '/v1/chat/completions', imageCompat: true, rpm: 120, rateUnit: 'minute', cacheTtl: '1h0m0s', enabled: true, keyConfigured: true, keyCount: 2, builtin: false },
  { id: 'sigma-llm', name: 'Sigma LLM', baseUrl: 'https://sigma-llm.example/v1', authMode: 'bearer', authHeader: 'Authorization', dialect: 'openai', modelsPath: '/v1/models', format: 'chat', chatPath: '/v1/chat/completions', imageCompat: false, rpm: 8, rateUnit: 'second', cacheTtl: '0s', enabled: true, keyConfigured: true, keyCount: 1, builtin: false },
  { id: 'spare', name: 'Spare quota', baseUrl: 'https://backup.llm.example/v1', authMode: 'passthrough', authHeader: 'Authorization', dialect: 'auto', modelsPath: '/v1/models', format: 'auto', chatPath: '/v1/chat/completions', imageCompat: false, rpm: 0, rateUnit: 'minute', cacheTtl: '0s', enabled: false, keyConfigured: false, keyCount: 0, builtin: false },
]

const modelIds = ['glm-5.3', 'glm-5.3-prime', 'qwen3.8-max', 'qwen3.8-max-0902', 'kimi-k3', 'deepseek-v4-pro-0813', 'deepseek-v4-flash', 'qwen-plus']

function fixtureRequest(index: number, offsetMs: number): ActivityRequest {
  const states: ActivityRequest['state'][] = ['active', 'completed', 'completed', 'completed', 'retrying', 'completed', 'failed', 'completed', 'cancelled', 'completed']
  const state = states[index % states.length]
  const model = modelIds[index % modelIds.length]
  const provider = providers[index % 3]
  const input = 12_000 + ((index * 43_777) % 380_000)
  const output = state === 'failed' ? 0 : 300 + ((index * 911) % 6_400)
  const cached = Math.floor(input * (index % 4 === 0 ? 0.7 : 0.18))
  return {
    id: `req-${String(index).padStart(4, '0')}-${(now - offsetMs).toString(36)}`,
    startedAt: iso(offsetMs + 3_200),
    updatedAt: iso(offsetMs),
    state,
    model,
    providerId: provider.id,
    providerName: provider.name,
    method: 'POST',
    path: '/v1/responses',
    status: state === 'failed' ? 429 : state === 'completed' ? 200 : undefined,
    queueMs: index % 5 === 0 ? 1_400 + index * 13 : 0,
    latencyMs: state === 'active' ? 8_000 + index * 97 : 1_900 + ((index * 613) % 24_000),
    retries: state === 'retrying' ? 1 : state === 'failed' ? 2 : 0,
    bytesIn: input * 4,
    bytesOut: output * 4,
    errorCode: state === 'failed' ? 'rate_limited' : undefined,
    errorDetail: state === 'failed' ? 'upstream 429: rpm limit reached for this key; request was retried on the next key of the pool' : undefined,
    inputTokens: input,
    outputTokens: output,
    cachedTokens: cached,
    reasoningTokens: model.includes('glm') || model.includes('kimi') ? Math.floor(output * 0.35) : 0,
    totalTokens: input + output,
    contextTokens: input + cached,
    generationMs: 1_400 + ((index * 271) % 9_000),
    tokensPerSecond: state === 'active' ? 41 + (index % 30) : 0,
  }
}

const activity: ActivityRequest[] = Array.from({ length: 42 }, (_, index) => fixtureRequest(index, index * 96_000 + 4_000))

const relayRoutes: ModelRoute[] = [
  { target: 'relay', publicModel: 'glm', upstreamModel: 'glm-5.3', providerId: 'north-relay', contextLimitKiB: 0, aliases: [], enabled: true, priority: 0 },
  { target: 'relay', publicModel: 'glm', upstreamModel: 'glm-5.3', providerId: 'vendor-hub', contextLimitKiB: 0, aliases: [], enabled: true, priority: 1 },
  { target: 'relay', publicModel: 'glm', upstreamModel: 'glm-5.3-flash', providerId: 'sigma-llm', contextLimitKiB: 0, aliases: ['glm-fast'], enabled: true, priority: 2 },
  { target: 'relay', publicModel: 'kimi', upstreamModel: 'kimi-k3', providerId: 'vendor-hub', contextLimitKiB: 0, aliases: [], enabled: true, priority: 0 },
  { target: 'relay', publicModel: 'qwen3.8-max', upstreamModel: 'qwen3.8-max-0902', providerId: 'north-relay', contextLimitKiB: 0, aliases: [], enabled: false, priority: 0 },
]
const tunnelRoutes: ModelRoute[] = [
  { target: 'tunnel', publicModel: 'fast', upstreamModel: 'glm-5.3-flash', providerId: 'sigma-llm', contextLimitKiB: 65_536, aliases: [], enabled: true },
  { target: 'tunnel', publicModel: 'smart', upstreamModel: 'glm-5.3', providerId: 'north-relay', contextLimitKiB: 131_072, aliases: [], enabled: true },
]

const settings: Settings = {
  listenerPort: 8787,
  maxRequestMiB: 48,
  headerTimeoutSeconds: 60,
  streamIdleSeconds: 300,
  retryBaseMilliseconds: 350,
  retryMaxSeconds: 30,
  permanentAttempts: 2,
  maxQueued: 5000,
  activityCapacity: 2000,
  historyRetentionDays: 90,
  tunnelRetentionHours: 168,
  guardrailMode: 'monitor',
  guardrailProviderModes: { 'sigma-llm': 'block' },
  guardrailFindings: 500,
  notificationsEnabled: true,
  providerHealthEnabled: true,
  animationsEnabled: true,
  failoverEnabled: true,
  chainMode: 'balance',
}

const findings: GuardrailRecord[] = Array.from({ length: 9 }, (_, index) => {
  const samples: Pick<GuardrailRecord, 'verdict' | 'severity' | 'findings'>[] = [
    { verdict: 'alert', severity: 'high', findings: [{ ruleId: 'net-curl-pipe-sh', category: 'shell-exec', severity: 'high', match: 'curl x | sh', excerpt: 'Assistant suggested piping a downloaded script into sh.', source: 'assistant text', description: 'Downloaded script executed directly' }] },
    { verdict: 'alert', severity: 'medium', findings: [{ ruleId: 'ioc-word', category: 'ioc', severity: 'medium', match: 'proxy.exe', excerpt: 'Mention of a proxy helper binary in tool arguments.', source: 'tool call arguments', description: 'Known campaign file name' }] },
    { verdict: 'blocked', severity: 'high', findings: [{ ruleId: 'obf-decode-pipe-exec', category: 'obfuscation', severity: 'high', match: 'base64 -d | sh', excerpt: 'Decode-then-execute chain in a shell command.', source: 'assistant text', description: 'Encoded payload piped to a shell' }] },
  ]
  const sample = samples[index % samples.length]
  const provider = providers[index % 3]
  return {
    id: `finding-${index}`,
    at: iso(index * 3_600_000),
    verdict: sample.verdict,
    severity: sample.severity,
    providerId: provider.id,
    providerName: provider.name,
    model: modelIds[index % modelIds.length],
    findings: sample.findings,
    occurrences: index % 4 === 3 ? 4 : 1,
  }
})

function volume(seed: number, requests: number) {
  const inputTokens = requests * (48_000 + seed * 3_700)
  const outputTokens = Math.floor(requests * (900 + seed * 61))
  return {
    requests,
    completed: Math.floor(requests * 0.93),
    failed: Math.floor(requests * 0.05),
    cancelled: Math.floor(requests * 0.02),
    retries: Math.floor(requests * 0.09),
    inputTokens,
    outputTokens,
    cachedTokens: Math.floor(inputTokens * 0.42),
    reasoningTokens: Math.floor(outputTokens * 0.3),
    totalTokens: inputTokens + outputTokens,
    generationMs: requests * 4_200,
    cost: (inputTokens * 0.0000011 + outputTokens * 0.0000044) * (1 + seed * 0.2),
    isPriced: true,
  }
}

function reportFor(period: string): InsightsReport {
  const days = period === '24h' ? 1 : period === '48h' ? 2 : period === '72h' ? 3 : 30
  const total = Math.max(days, 14)
  const daily = Array.from({ length: total }, (_, index) => {
    const date = new Date(now - (total - 1 - index) * 86_400_000)
    const requests = 40 + ((index * 37) % 190)
    return { date: date.toISOString().slice(0, 10), volume: volume(index, requests), successRate: 0.9 + (index % 5) * 0.018, tokensPerSecond: 38 + (index % 20) }
  })
  return {
    period: period as InsightsReport['period'],
    generatedAt: iso(0),
    overview: { volume: volume(3, 486), successRate: 0.962, p50Ms: 2_140, p95Ms: 18_600, tokensPerSecond: 44, pricedRequests: 470, topErrorCode: 'rate_limited' },
    providers: [
      { id: 'north-relay', name: 'North Relay', volume: volume(1, 240), p50Ms: 1_900, p95Ms: 12_400, avgMs: 2_700, tokensPerSecond: 48, errors: [{ errorCode: 'rate_limited', requests: 9 }] },
      { id: 'vendor-hub', name: 'Vendor Hub', volume: volume(2, 170), p50Ms: 2_600, p95Ms: 22_100, avgMs: 3_900, tokensPerSecond: 39, errors: [{ errorCode: 'stream_idle_timeout', requests: 4 }] },
      { id: 'sigma-llm', name: 'Sigma LLM', volume: volume(4, 76), p50Ms: 1_400, p95Ms: 9_800, avgMs: 2_100, tokensPerSecond: 52, errors: [] },
    ],
    models: [
      { model: 'glm-5.3', providerName: 'North Relay', volume: volume(1, 210), p50Ms: 1_800, p95Ms: 11_900, avgMs: 2_500, tokensPerSecond: 49, errors: [] },
      { model: 'qwen3.8-max-0902', providerName: 'North Relay', volume: volume(2, 96), p50Ms: 2_200, p95Ms: 14_700, avgMs: 3_100, tokensPerSecond: 44, errors: [] },
      { model: 'kimi-k3', providerName: 'Vendor Hub', volume: volume(3, 88), p50Ms: 2_900, p95Ms: 24_200, avgMs: 4_400, tokensPerSecond: 37, errors: [{ errorCode: 'stream_idle_timeout', requests: 3 }] },
      { model: 'deepseek-v4-pro-0813', providerName: 'Vendor Hub', volume: volume(5, 62), p50Ms: 3_100, p95Ms: 26_400, avgMs: 4_900, tokensPerSecond: 34, errors: [] },
      { model: 'qwen-plus', providerName: 'Sigma LLM', volume: { ...volume(6, 30), isPriced: false, cost: 0 }, p50Ms: 1_200, p95Ms: 8_200, avgMs: 1_900, tokensPerSecond: 55, errors: [] },
    ],
    daily,
    errors: [
      { errorCode: 'rate_limited', requests: 14 },
      { errorCode: 'stream_idle_timeout', requests: 7 },
      { errorCode: 'provider_unreachable', requests: 2 },
    ],
    unpricedModels: ['qwen-plus'],
  }
}

const history: HistoryRequest[] = activity.slice(0, 30).map((request) => ({
  id: request.id,
  state: request.state,
  model: request.model,
  providerId: request.providerId,
  status: request.status ?? 200,
  latencyMs: request.latencyMs,
  totalTokens: request.totalTokens,
  cachedTokens: request.cachedTokens,
  updatedAt: request.updatedAt,
  errorCode: request.errorCode ?? '',
  errorDetail: request.errorDetail ?? '',
}))

const prices: ModelPrice[] = [
  { model: 'glm-5.3', input: 0.6, cachedInput: 0.11, output: 2.2, reasoning: 2.2, updatedAt: iso(86_400_000) },
  { model: 'qwen3.8-max-0902', input: 1.2, cachedInput: 0.24, output: 6, reasoning: 0, updatedAt: iso(172_800_000) },
]

// The catalog's unit of account follows setCurrency like the real store does.
let catalogCurrency = 'USD'

export class FixtureSession implements ControlPlaneSession {
  readonly appVersion = '1.0.37-fixture'
  readonly #listeners = new Map<string, Set<EventListener>>()
  readonly #runTimers = new Set<number>()
  #tick = 0
  #seq = 0
  #timer: ReturnType<typeof setInterval> | undefined

  start(): Promise<void> {
    // Live traffic so Live Activity and the Overview move on their own.
    this.#timer = setInterval(() => {
      this.#tick++
      const request = fixtureRequest(100 + this.#tick, 0)
      this.#emit('activity.changed', request)
    }, 2_400)
    return Promise.resolve()
  }

  stop(): Promise<void> {
    clearInterval(this.#timer)
    this.#listeners.clear()
    return Promise.resolve()
  }

  subscribe<T>(topic: string, listener: EventListener<T>): () => void {
    const set = this.#listeners.get(topic) ?? new Set()
    set.add(listener as EventListener)
    this.#listeners.set(topic, set)
    return () => set.delete(listener as EventListener)
  }

  call<T>(method: string, payload?: unknown, signal?: AbortSignal): Promise<T> {
    try {
      const answer = this.#answer(method, payload) as T
      // The long-running answer (a test run) honors cancellation the way the
      // real session does: an abort rejects it and stops the pending events.
      if (answer instanceof Promise) {
        return new Promise<T>((resolve, reject) => {
          answer.then(resolve, reject)
          signal?.addEventListener('abort', () => {
            for (const timer of this.#runTimers) window.clearTimeout(timer)
            this.#runTimers.clear()
            reject(new DOMException('Aborted', 'AbortError'))
          }, { once: true })
        })
      }
      return Promise.resolve(answer)
    } catch (error) {
      return Promise.reject(error instanceof Error ? error : new Error(String(error)))
    }
  }

  #emit(topic: string, payload: unknown): void {
    this.#seq++
    const frame: EventFrame = { v: 1, type: 'event', topic, seq: this.#seq, payload }
    for (const listener of this.#listeners.get(topic) ?? []) listener(frame)
  }

  #answer(method: string, payload?: unknown): unknown {
    const body = (payload ?? {}) as Record<string, never>
    switch (method) {
      case 'relay.status': return { state: 'live', address: 'http://127.0.0.1:8787', port: 8787 }
      case 'updates.check': return { current: this.appVersion, latest: this.appVersion, url: '', newer: false, reachable: true, checkedAt: iso(0) }
      case 'relay.start': case 'relay.stop': return { state: 'live', address: 'http://127.0.0.1:8787', port: 8787 }
      case 'providers.list': return { activeId: 'north-relay', providers }
      case 'providers.health': return { states: providers.map((provider) => ({ providerId: provider.id, up: provider.enabled, reason: provider.enabled ? '' : 'Disabled' })) }
      case 'activity.list': return { requests: activity, available: activity.length }
      case 'activity.summary': return { requests: 486, active: 2, queued: 1, successRate: 96.2, p95Ms: 18_600, rpm: 12.4 }
      case 'settings.get': return settings
      case 'settings.update': return { settings: { ...settings, ...body }, restartRequired: false }
      case 'analytics.report': return reportFor(String(body.period ?? '24h'))
      case 'history.recent': return { requests: history, available: 486 }
      case 'analytics.prices.get': return { prices, currency: catalogCurrency }
      case 'analytics.prices.set': return { prices, currency: catalogCurrency }
      case 'analytics.prices.remove': return { prices: prices.slice(1), currency: catalogCurrency }
      case 'routes.list': return { routes: body.target === 'tunnel' ? tunnelRoutes : relayRoutes }
      case 'models.discover': return { models: modelIds.concat(['glm-5.3-flash', 'glm-5.2', 'qwen3.7-max', 'qwen3.7-plus', 'kimi-k2.7-code', 'deepseek-v4.1-flash', 'qwen3.5-plus', 'deepseek-v3.2']) }
      case 'analytics.prices.setCurrency': catalogCurrency = String(body.currency ?? 'USD'); return { prices, currency: catalogCurrency }
      case 'models.test': {
        // The probe's answers arrive as events over a beat, the way the real
        // sidecar reports them — a UI that only renders on command completion
        // shows nothing in the fixture either.
        const runId = String(body.runId ?? '')
        const providerId = String(body.providerId ?? '')
        const models = Array.isArray(body.models) ? (body.models as readonly string[]) : []
        // Per-provider seed: identical rows across providers would read as
        // copied data, and the page exists to compare them.
        const seed = providerId.split('').reduce((sum, char) => sum + char.charCodeAt(0), 0)
        models.forEach((model, index) => {
          const timer = window.setTimeout(() => {
            this.#runTimers.delete(timer)
            const failed = model.includes('flash') && providerId === 'sigma-llm'
            const ttft = 380 + ((index * 211 + seed * 7) % 2_600)
            const total = ttft + 700 + ((index * 503 + seed * 31) % 4_000)
            this.#emit('models.tested', {
              runId, providerId, model,
              state: failed ? 'unavailable' : 'available',
              status: failed ? 429 : 200,
              latencyMs: total,
              ttftMs: failed ? 0 : ttft,
              outputTokens: failed ? 0 : 32,
              ...(failed ? { errorCode: 'rate_limited' } : {}),
            })
          }, 320 * (index + 1))
          this.#runTimers.add(timer)
        })
        const pending = new Promise<{ tested: number }>((resolve) => {
          window.setTimeout(() => resolve({ tested: models.length }), 320 * (models.length + 1))
        })
        return pending
      }
      case 'keys.list': return { keys: [
        { id: 'k1', providerId: body.providerId, label: 'primary', priority: 0, rpm: 40, pinned: false, proxyConfigured: false, cooldownMs: 0, blockedModels: 0, retries429: 3, startsInWindow: 11, authStreak: 0, lastOutcome: 'success' },
        { id: 'k2', providerId: body.providerId, label: 'backup-2', priority: 1, rpm: 40, pinned: false, proxyConfigured: true, cooldownMs: 42_000, blockedModels: 0, retries429: 14, startsInWindow: 4, authStreak: 1, lastOutcome: 'rate-limited' },
        { id: 'k3', providerId: body.providerId, label: 'old-reseller', priority: 2, rpm: 0, pinned: false, proxyConfigured: false, cooldownMs: 0, blockedModels: 2, retries429: 91, startsInWindow: 0, authStreak: 5, lastOutcome: 'authentication' },
        { id: 'k4', providerId: body.providerId, label: 'pinned-ip', priority: 3, rpm: 20, pinned: true, proxyConfigured: false, cooldownMs: 0, blockedModels: 0, retries429: 0, startsInWindow: 2, authStreak: 0, lastOutcome: 'success' },
      ] }
      case 'keys.check': return { checked: 4, rejected: 1, reachable: true }
      case 'guardrails.status': return { mode: 'monitor', providerModes: { 'sigma-llm': 'block' }, ruleCount: 106, indicatorCount: 1_900, ruleSetVersion: 7, findingCount: 12 }
      case 'guardrails.findings': return { findings }
      case 'tunnel.get': case 'tunnel.configure': case 'tunnel.start': return { state: 'online', port: 8797, address: 'https://fresh-words-mild-orbit.trycloudflare.com/v1', rpmPerIp: 60, contextLimitKiB: 131_072, brandResponse: '', tokenConfigured: true }
      case 'tunnel.stop': return { state: 'stopped', port: 8797, address: '', rpmPerIp: 60, contextLimitKiB: 131_072, brandResponse: '', tokenConfigured: true }
      case 'tunnel.rotate': case 'tunnel.reveal': return { token: 'swt_fixture_access_key_0123456789abcdef' }
      case 'tunnel.privacy_test': return { checkedAt: iso(0), requestUrl: 'https://switchboard-v1.21433.examplehost.dev/v1/models', status: 200, statusText: 'OK', protocol: 'HTTP/2', remoteAddress: '104.21.5.19:443', durationMs: 214, bodyBytes: 841, headers: [
        { name: 'content-type', values: ['application/json'] },
        { name: 'server', values: ['cloudflare'] },
      ], topLevelFields: ['object', 'data'], models: [
        { id: 'fast', object: 'model', created: 1_721_664_000, fields: ['id', 'object', 'created', 'owned_by'] },
        { id: 'smart', object: 'model', created: 1_721_664_000, fields: ['id', 'object', 'created', 'owned_by'] },
      ], rawBody: '{"object":"list","data":[{"id":"fast","object":"model","created":1721664000,"owned_by":"library"},{"id":"smart","object":"model","created":1721664000,"owned_by":"library"}]}', credentialReflected: false, tls: { version: 'TLSv1.3', cipherSuite: 'TLS_AES_128_GCM_SHA256', serverName: 'switchboard-v1.21433.examplehost.dev', certificateSubject: 'CN=examplehost.dev', certificateIssuer: 'C=US, O=Let\'s Encrypt, CN=E1', certificateExpiresAt: iso(-5_184_000_000) } }
      case 'clients.list': return { clients: [
        { ip: '203.0.113.24', actualRpm: 41, count: 1_204, active: 2, queued: 0, refused: 3, lastSeen: iso(60_000), state: 'active', banned: false, note: 'Coding rig' },
        { ip: '198.51.100.7', actualRpm: 6, count: 88, active: 0, queued: 1, refused: 0, lastSeen: iso(300_000), state: 'idle', banned: false, note: '' },
        { ip: '2001:db8::42', actualRpm: 0, count: 2_940, active: 0, queued: 0, refused: 640, lastSeen: iso(7_200_000), state: 'idle', banned: true, note: 'Scraper, kept banned' },
        { ip: '192.0.2.150', actualRpm: 12, count: 512, active: 1, queued: 0, refused: 0, lastSeen: iso(12_000), state: 'active', banned: false, note: 'Phone' },
      ] }
      case 'clients.events': return { events: activity.slice(0, 14).map((request) => ({ id: request.id, ip: String(body.ip ?? ''), time: request.updatedAt, state: request.state === 'failed' ? 'error' : 'complete', method: request.method, path: request.path, model: request.model, status: request.status ?? 200, latencyMs: request.latencyMs, bytesIn: request.bytesIn, bytesOut: request.bytesOut, errorCode: request.errorCode })) }
      case 'notifications.list': return { notifications: [
        { id: 'n1', kind: 'keys', severity: 'warning', title: 'Key "old-reseller" was rejected', body: 'North Relay answered 401 three times in a row. The key is skipped until reset.', at: iso(3_600_000) },
        { id: 'n2', kind: 'relay', severity: 'info', title: 'Relay is live', body: 'Listening on http://127.0.0.1:8787.', at: iso(7_200_000) },
        { id: 'n3', kind: 'backup', severity: 'success', title: 'Backup exported', body: 'Providers, keys and routes written to disk.', at: iso(86_400_000) },
      ] }
      // A command the fixture never stubbed must fail loudly in the dev
      // console, not silently succeed with an empty object.
      default: throw new Error(`fixture session has no answer for ${method}`)
    }
  }
}
