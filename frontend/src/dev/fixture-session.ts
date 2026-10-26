/**
 * Development fixture session: answers every control-plane command with
 * realistic local data and emits live activity ticks, so the interface can be
 * exercised in a plain browser via `pnpm dev` + `?fixture=1`. It is reachable
 * only through a dynamic import behind `import.meta.env.DEV`; production builds
 * tree-shake the branch and never bundle it.
 */
import { version as appVersion } from '../../package.json'
import type { EventFrame } from '../shared/contracts/protocol'
import type { ControlPlaneSession, EventListener } from '../platform/stdio/session'
import type { ActivityRequest } from '../features/activity/domain/activity'
import type { Provider } from '../features/providers/domain/provider'
import type { ModelRoute } from '../features/model-routes/domain/route'
import type { Settings } from '../features/settings/domain/settings'
import type { GuardrailRecord } from '../features/guardrails/domain/guardrail'
import type { InsightsReport, HistoryRequest, ModelPrice } from '../features/insights/domain/insights'
import { ControlPlaneError } from '../shared/contracts/protocol'
import { PLUGIN_LIMIT, checkPackage, needsNewerApp, type PluginPackage } from '../features/plugins/domain/manifest'
import { FixtureRules } from './fixture-rules'

interface FixturePlugin {
  package: PluginPackage
  granted: string[]
  enabled: boolean
  installedAt: string
  updatedAt: string
  storage: Record<string, string>
  secrets?: Record<string, string>
}

const FIXTURE_PLUGINS_KEY = 'switchboard.fixture.plugins'

function loadFixturePlugins(): Map<string, FixturePlugin> {
  const plugins = new Map<string, FixturePlugin>()
  try {
    const stored = JSON.parse(window.localStorage.getItem(FIXTURE_PLUGINS_KEY) ?? '[]') as unknown
    for (const record of Array.isArray(stored) ? stored as FixturePlugin[] : []) {
      const checked = checkPackage(record?.package)
      if (checked.ok) plugins.set(checked.package.id, { ...record, package: checked.package })
    }
  } catch { /* a damaged dev record starts empty */ }
  return plugins
}

const now = Date.now()
const iso = (offsetMs: number): string => new Date(now - offsetMs).toISOString()

// Fixture providers are fictional on purpose: the file is committed, and a
// development fixture is not where real vendor names belong.
const providers: Provider[] = [
  { id: 'north-relay', name: 'North Relay', baseUrl: 'https://api.north-relay.example/v1', authMode: 'bearer', authHeader: 'Authorization', dialect: 'openai', modelsPath: '/v1/models', format: 'chat', chatPath: '/v1/chat/completions', imageCompat: false, rpm: 40, rateUnit: 'minute', cacheTtl: '0s', enabled: true, keyConfigured: true, keyCount: 3, builtin: false },
  { id: 'vendor-hub', name: 'Vendor Hub', baseUrl: 'https://router.vendor-hub.example/v1', authMode: 'bearer', authHeader: 'Authorization', dialect: 'auto', modelsPath: '/v1/models', format: 'responses', chatPath: '/v1/chat/completions', imageCompat: true, rpm: 120, rateUnit: 'minute', cacheTtl: '1h0m0s', enabled: true, keyConfigured: true, keyCount: 2, builtin: false },
  { id: 'sigma-llm', name: 'Sigma LLM', baseUrl: 'https://sigma-llm.example/v1', authMode: 'bearer', authHeader: 'Authorization', dialect: 'openai', modelsPath: '/v1/models', format: 'chat', chatPath: '/v1/chat/completions', imageCompat: false, rpm: 8, rateUnit: 'second', cacheTtl: '0s', enabled: true, keyConfigured: true, keyCount: 1, builtin: false },
  { id: 'spare', name: 'Spare quota', baseUrl: 'https://backup.llm.example/v1', authMode: 'passthrough', authHeader: 'Authorization', dialect: 'auto', modelsPath: '/v1/models', format: 'auto', chatPath: '/v1/chat/completions', imageCompat: false, rpm: 0, rateUnit: 'minute', cacheTtl: '0s', enabled: false, keyConfigured: false, keyCount: 0, builtin: false },
  { id: 'codex', name: 'Codex', baseUrl: 'https://api.codex.example/v1', authMode: 'bearer', authHeader: 'Authorization', dialect: 'openai', modelsPath: '/v1/models', format: 'responses', chatPath: '/v1/responses', imageCompat: false, rpm: 60, rateUnit: 'minute', cacheTtl: '0s', enabled: true, keyConfigured: false, keyCount: 0, builtin: false, preset: 'codex' },
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
    // Like the relay: a rate only for a completed answer, a first token for
    // any answer that streamed one.
    tokensPerSecond: state === 'completed' ? 41 + (index % 30) + (index % 7) / 10 : 0,
    ...(state === 'completed' || state === 'active' ? { ttftMs: 280 + ((index * 389) % 2_600) } : {}),
    // The level each client dialect sends: an effort name, a token budget,
    // or nothing at all (the provider's default).
    ...fixtureReasoning(index, state, output),
    // Labels from request rules and relay hooks, on some rows only.
    ...(index % 3 === 0 ? { tags: index % 2 === 0 ? ['team.alpha', 'batch'] : ['nightly'] } : {}),
  }
}

function fixtureReasoning(index: number, state: ActivityRequest['state'], output: number): Partial<ActivityRequest> {
  const levels: readonly Pick<ActivityRequest, 'reasoningEffort' | 'reasoningBudget'>[] = [
    { reasoningEffort: 'high' },
    {},
    { reasoningEffort: 'medium' },
    { reasoningEffort: 'on', reasoningBudget: 16_000 },
    { reasoningEffort: 'xhigh' },
    { reasoningEffort: 'low' },
    {},
  ]
  const level = levels[index % levels.length]
  if (!level.reasoningEffort) return level
  return {
    ...level,
    // Live rows alternate between the reasoning phase and the answer.
    thinking: state === 'active' && Math.floor(index / 10) % 2 === 0,
    thinkingMs: state === 'failed' ? 0 : 600 + ((index * 389 + output) % 14_000),
  }
}

const generated: ActivityRequest[] = Array.from({ length: 42 }, (_, index) => fixtureRequest(index, index * 96_000 + 4_000))
// Requests that generate nothing: a prompt-size count and a client's probe.
const nonGeneration: ActivityRequest[] = [
  { ...generated[2], id: `req-count-${now.toString(36)}`, model: 'claude-fixture', path: '/v1/messages/count_tokens', status: 200, queueMs: 0, latencyMs: 10, retries: 0, bytesIn: 41_779, bytesOut: 23, inputTokens: 0, outputTokens: 0, cachedTokens: 0, reasoningTokens: 0, totalTokens: 0, contextTokens: 10_412, generationMs: 0, tokensPerSecond: 0, ttftMs: undefined, tags: undefined, reasoningEffort: undefined, reasoningBudget: undefined, thinking: undefined, thinkingMs: undefined },
  { ...generated[6], id: `req-probe-${now.toString(36)}`, model: '', method: 'HEAD', path: '/api/hello', status: 404, queueMs: 0, latencyMs: 275, retries: 1, bytesIn: 0, bytesOut: 46, errorCode: 'upstream_status', errorDetail: 'Provider answered 404', inputTokens: 0, outputTokens: 0, cachedTokens: 0, reasoningTokens: 0, totalTokens: 0, contextTokens: 0, generationMs: 0, tokensPerSecond: 0, ttftMs: undefined, tags: undefined, reasoningEffort: undefined, reasoningBudget: undefined, thinking: undefined, thinkingMs: undefined },
]
const activity: ActivityRequest[] = [...generated.slice(0, 3), ...nonGeneration, ...generated.slice(3)]

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
  maxRequestMiB: 128,
  headerTimeoutSeconds: 300,
  streamIdleSeconds: 300,
  heartbeatSeconds: 30,
  retryBaseMilliseconds: 350,
  retryMaxSeconds: 30,
  permanentAttempts: 2,
  maxQueued: 5000,
  streamProbationMilliseconds: 400,
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
  updateCheckInterval: '1m',
}

// The standing verdict of the refresher's last automatic pass: old enough that
// the panel shows an honest "2 minutes ago", fresh enough that the pill is on
// the moment the dev shell opens.
const screenshotMode = new URLSearchParams(window.location.search).has('screenshots')
const updateFeed = {
  current: appVersion,
  latest: screenshotMode ? appVersion : '1.0.99',
  url: `https://github.com/GofMan5/luxury-switchboard/releases/tag/v${screenshotMode ? appVersion : '1.0.99'}`,
  newer: !screenshotMode,
  reachable: true,
  checkedAt: iso(120_000),
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
  reasoningEffort: request.reasoningEffort,
  reasoningBudget: request.reasoningBudget,
}))

const prices: ModelPrice[] = [
  { model: 'glm-5.3', input: 0.6, cachedInput: 0.11, output: 2.2, reasoning: 2.2, updatedAt: iso(86_400_000) },
  { model: 'qwen3.8-max-0902', input: 1.2, cachedInput: 0.24, output: 6, reasoning: 0, updatedAt: iso(172_800_000) },
]

// The catalog's unit of account follows setCurrency like the real store does.
let catalogCurrency = 'USD'

export class FixtureSession implements ControlPlaneSession {
  readonly appVersion = appVersion
  /** The fixture advertises the Codex preset flow, its usage probes and the
   * fleet wake, like a current control plane. */
  readonly capabilities = ['codex.login', 'codex.quota', 'codex.quota.all', 'codex.wake', 'appearance.manage', 'plugins.manage', 'plugins.download', 'rules.manage']
  readonly #listeners = new Map<string, Set<EventListener>>()
  readonly #rules = new FixtureRules((topic, payload) => this.#emit(topic, payload))
  // The appearance store survives a dev reload in browser storage, like the
  // encrypted record survives a restart of the real control plane.
  #appearance: Record<string, unknown> = (() => {
    try { return JSON.parse(window.localStorage.getItem('switchboard.fixture.appearance') ?? '{}') as Record<string, unknown> } catch { return {} }
  })()
  readonly #runTimers = new Set<number>()
  #tick = 0
  #seq = 0
  #timer: ReturnType<typeof setInterval> | undefined
  // The preset starts with no accounts; a dev sign-in walks the real
  // waiting → exchanging → success curve on timers, like a browser OAuth hop.
  // The second sign-in grants a second account, so the dev shell has a
  // multi-account list to show.
  #codexPhase: 'idle' | 'waiting' | 'exchanging' | 'success' | 'error' = 'idle'
  #codexError = ''
  #codexAccounts: { state: 'signed_out' | 'signed_in' | 'reauth_needed'; email: string; plan: string; accountId: string; providerId: string; loginMethod: 'device' | 'browser'; resets: number }[] = []
  #codexTimers = new Set<number>()

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
    this.#codexCancelTimers()
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

  #codexBeginLogin(): void {
    this.#codexCancelTimers()
    this.#codexPhase = 'waiting'
    this.#codexError = ''
    this.#emit('codex.changed', {})
    this.#codexTimers.add(window.setTimeout(() => {
      this.#codexPhase = 'exchanging'
      this.#emit('codex.changed', {})
    }, 4_000))
    this.#codexTimers.add(window.setTimeout(() => {
      this.#codexPhase = 'success'
      // The first sign-in connects alex; the next one adds sam, so the
      // dev shell walks the same multi-account path real usage does.
      if (!this.#codexAccounts.some((account) => account.accountId === 'acct_fixture_codex')) {
        this.#codexGrant('alex@example.com', 'Plus', 'acct_fixture_codex')
      } else {
        // The second account signs in over the device flow and carries a
        // nonzero reset count, so the dev shell shows both columns the real
        // multi-account list has.
        this.#codexGrant('sam@example.com', 'Pro', 'acct_fixture_codex_2', 'device', 3)
      }
      this.#emit('codex.changed', {})
    }, 6_500))
  }

  #codexGrant(email: string, plan: string, accountId: string, loginMethod: 'device' | 'browser' = 'browser', resets = 0): void {
    const existing = this.#codexAccounts.find((account) => account.accountId === accountId)
    if (existing !== undefined) {
      existing.state = 'signed_in'
      existing.email = email
      existing.plan = plan
      existing.providerId = 'codex'
      existing.loginMethod = loginMethod
      existing.resets = resets
      return
    }
    this.#codexAccounts.push({ state: 'signed_in', email, plan, accountId, providerId: 'codex', loginMethod, resets })
  }

  // The status answer is the aggregate over rows, exactly like the backend:
  // signed in once any account is, reauth when every signed-in one broke,
  // signed out otherwise.
  #codexStatus(): { state: 'signed_out' | 'signed_in' | 'reauth_needed'; accounts: unknown[] } {
    const state = this.#codexAccounts.some((account) => account.state === 'signed_in')
      ? 'signed_in'
      : this.#codexAccounts.some((account) => account.state === 'reauth_needed')
        ? 'reauth_needed'
        : 'signed_out'
    return { state, accounts: this.#codexAccounts.map((account) => ({ ...account })) }
  }

  // One probe answer, shared by the single and the fleet path: usage windows
  // ride a fresh clock so the reset stamps in the dev UI always read as
  // plausible local times. Two live meters keep the rows distinguishable.
  #codexQuota(account: { accountId: string; plan: string }): { fetchedAt: number; planType: string; primary: { present: boolean; remainingPercent: number; windowMinutes: number; resetAt: number }; secondary: { present: boolean; remainingPercent: number; windowMinutes: number; resetAt: number } } {
    const now = Math.floor(Date.now() / 1000)
    const primary = account.accountId === 'acct_fixture_codex_2' ? 23 : 76
    const secondary = account.accountId === 'acct_fixture_codex_2' ? 68 : 41
    return {
      fetchedAt: now,
      planType: account.plan !== '' ? account.plan : 'Pro',
      primary: { present: true, remainingPercent: primary, windowMinutes: 300, resetAt: now + 3120 },
      secondary: { present: true, remainingPercent: secondary, windowMinutes: 10080, resetAt: now + 172_800 },
    }
  }

  #codexCancel(): void {
    this.#codexCancelTimers()
    this.#codexPhase = 'idle'
    this.#codexError = ''
    this.#emit('codex.changed', {})
  }

  #codexCancelTimers(): void {
    for (const timer of this.#codexTimers) window.clearTimeout(timer)
    this.#codexTimers.clear()
  }

  // Imports land signed-in in one step in the fixture; the model still walks
  // its connecting → exchanging → success curve around the promise, so the
  // dev shell shows the same phases the real sidecar produces.
  #codexImportResult(plan: string) {
    this.#codexGrant('alex@example.com', plan, 'acct_fixture_codex')
    this.#emit('codex.changed', {})
    return this.#codexStatus()
  }

  // ── Plugins ──────────────────────────────────────────────────────────────
  // Installed packages and their storage survive a dev reload in browser
  // storage, like the encrypted records survive a control-plane restart. The
  // checks mirror the Go slice: same id rule, limits and error codes.
  #plugins: Map<string, FixturePlugin> = loadFixturePlugins()
  #activeProvider = 'north-relay'

  #pluginSave(): void {
    try { window.localStorage.setItem(FIXTURE_PLUGINS_KEY, JSON.stringify([...this.#plugins.values()])) } catch { /* dev only */ }
  }

  #pluginSummary(record: FixturePlugin) {
    const pkg = record.package
    const problem = needsNewerApp(pkg.minAppVersion ?? '', appVersion) ? `Needs Luxury Switchboard ${pkg.minAppVersion} or later; this is ${appVersion}. Update the app or install an older version of the plugin.` : ''
    return {
      id: pkg.id, name: pkg.name, version: pkg.version, author: pkg.author ?? '', description: pkg.description ?? '',
      homepage: pkg.homepage ?? '', minAppVersion: pkg.minAppVersion ?? '',
      permissions: [...pkg.permissions], granted: record.granted.filter((permission) => pkg.permissions.includes(permission as never)),
      enabled: record.enabled && !problem, installedAt: record.installedAt, updatedAt: record.updatedAt,
      sizeBytes: new TextEncoder().encode(JSON.stringify(pkg)).length,
      hasScript: Boolean(pkg.script?.trim()), hasCss: Boolean(pkg.css?.trim()), hasTheme: pkg.theme !== undefined, hasSettings: Boolean(pkg.settings?.length),
      problem, secrets: Object.keys(record.secrets ?? {}).sort(), hosts: pkg.network?.hosts ?? [],
    }
  }

  // ponytail: collections live in memory only; a dev reload starts empty.
  #collections = new Map<string, Map<string, Map<string, unknown>>>()

  #collection(body: Record<string, unknown>): Map<string, unknown> {
    const record = this.#pluginFind(body.id)
    const name = typeof body.collection === 'string' ? body.collection : ''
    if (!/^[a-z0-9][a-z0-9_-]{0,31}$/u.test(name)) throw new ControlPlaneError('invalid_plugin', 'Plugin collection name is invalid')
    const all = this.#collections.get(record.package.id) ?? new Map<string, Map<string, unknown>>()
    this.#collections.set(record.package.id, all)
    const docs = all.get(name) ?? new Map<string, unknown>()
    all.set(name, docs)
    return docs
  }

  #collectionAnswer(method: string, body: Record<string, unknown>): unknown {
    if (method === 'plugins.collection.stats') {
      const all = [...(this.#collections.get(this.#pluginFind(body.id).package.id) ?? new Map<string, Map<string, unknown>>())]
      const collections = all.filter(([, docs]) => docs.size > 0).map(([name, docs]) => ({ name, count: docs.size, bytes: new TextEncoder().encode(JSON.stringify([...docs])).length }))
      return { collections, bytes: collections.reduce((sum, item) => sum + item.bytes, 0), limitBytes: 2 * 1024 * 1024 }
    }
    const docs = this.#collection(body)
    const docId = typeof body.docId === 'string' ? body.docId : ''
    switch (method) {
      case 'plugins.collection.get': return { doc: docs.get(docId) ?? null }
      case 'plugins.collection.put':
        if (body.doc === null || body.doc === undefined) throw new ControlPlaneError('invalid_plugin', 'Plugin document must not be null')
        docs.set(docId, body.doc)
        return { ok: true }
      case 'plugins.collection.delete': return { deleted: docs.delete(docId) }
      case 'plugins.collection.clear': {
        const cleared = docs.size
        docs.clear()
        return { cleared }
      }
      default: {
        const prefix = typeof body.prefix === 'string' ? body.prefix : ''
        const after = typeof body.after === 'string' ? body.after : ''
        const limit = typeof body.limit === 'number' ? body.limit : 50
        const ids = [...docs.keys()].filter((id) => id.startsWith(prefix) && id > after).sort()
        const page = ids.slice(0, limit)
        return { items: page.map((id) => ({ id, doc: docs.get(id) })), ...(ids.length > limit ? { next: page.at(-1) } : {}) }
      }
    }
  }

  #pluginStore(record: FixturePlugin, changes: Record<string, unknown>, clear: boolean): Record<string, string> {
    const next = clear ? {} : { ...record.storage }
    for (const [key, value] of Object.entries(changes)) {
      if (!/^[A-Za-z0-9._:-]{1,64}$/u.test(key)) throw new ControlPlaneError('invalid_plugin', 'Plugin storage key is invalid')
      if (typeof value === 'string') next[key] = value
      else if (value === null) delete next[key]
      else throw new ControlPlaneError('invalid_plugin', 'Plugin storage value must be a string or null')
    }
    if (Object.keys(next).length > 64 || Object.values(next).some((value) => new TextEncoder().encode(value).length > 8192) || new TextEncoder().encode(JSON.stringify(next)).length > 65_536) {
      throw new ControlPlaneError('plugin_storage_full', 'Plugin storage is full')
    }
    record.storage = next
    this.#pluginSave()
    return { ...next }
  }

  #pluginList() {
    return [...this.#plugins.values()].sort((a, b) => a.package.name.localeCompare(b.package.name)).map((record) => this.#pluginSummary(record))
  }

  #pluginChanged(): void {
    this.#pluginSave()
    this.#emit('plugins.changed', { plugins: this.#pluginList() })
  }

  #pluginFind(id: unknown): FixturePlugin {
    const record = typeof id === 'string' ? this.#plugins.get(id) : undefined
    if (!record) throw new ControlPlaneError('plugin_not_found', 'Plugin is not installed')
    return record
  }

  #pluginAnswer(method: string, body: Record<string, unknown>): unknown {
    switch (method) {
      case 'plugins.list': return { plugins: this.#pluginList() }
      case 'plugins.get': {
        const record = this.#pluginFind(body.id)
        return { plugin: this.#pluginSummary(record), script: record.package.script ?? '', css: record.package.css ?? '', theme: record.package.theme ?? null, settings: record.package.settings ?? [] }
      }
      case 'plugins.install': {
        const checked = checkPackage(body.package)
        if (!checked.ok) throw new ControlPlaneError('invalid_plugin', `Plugin ${checked.problems[0] ?? 'package is invalid'}`)
        const pkg = checked.package
        const existing = this.#plugins.get(pkg.id)
        if (!existing && this.#plugins.size >= PLUGIN_LIMIT) throw new ControlPlaneError('plugin_limit', `At most ${PLUGIN_LIMIT} plugins can be installed`)
        const granted = Array.isArray(body.granted) ? body.granted.filter((permission): permission is string => typeof permission === 'string' && pkg.permissions.includes(permission as never)) : []
        if (needsNewerApp(pkg.minAppVersion ?? '', appVersion)) throw new ControlPlaneError('invalid_plugin', `Plugin minAppVersion needs Luxury Switchboard ${pkg.minAppVersion} or later`)
        // Milliseconds, like the control plane; a reinstall always gets a
        // later revision so the runtime restarts it.
        let stamp = new Date().toISOString()
        if (existing && Date.parse(stamp) <= Date.parse(existing.updatedAt)) stamp = new Date(Date.parse(existing.updatedAt) + 1).toISOString()
        const record: FixturePlugin = { package: pkg, granted, enabled: true, installedAt: existing?.installedAt ?? stamp, updatedAt: stamp, storage: existing?.storage ?? {} }
        this.#plugins.set(pkg.id, record)
        this.#pluginChanged()
        return { plugin: this.#pluginSummary(record) }
      }
      case 'plugins.update': {
        const record = this.#pluginFind(body.id)
        if (typeof body.enabled === 'boolean') record.enabled = body.enabled
        if (Array.isArray(body.granted)) record.granted = body.granted.filter((permission): permission is string => typeof permission === 'string' && record.package.permissions.includes(permission as never))
        this.#pluginChanged()
        return { plugin: this.#pluginSummary(record) }
      }
      case 'plugins.remove': {
        this.#pluginFind(body.id)
        this.#plugins.delete(body.id as string)
        this.#collections.delete(body.id as string)
        this.#pluginChanged()
        return { removed: true }
      }
      case 'plugins.storage.get': return { values: { ...this.#pluginFind(body.id).storage } }
      case 'plugins.storage.set': {
        const record = this.#pluginFind(body.id)
        const key = typeof body.key === 'string' ? body.key : ''
        return { values: this.#pluginStore(record, { [key]: body.value === undefined ? 0 : body.value }, false) }
      }
      case 'plugins.storage.update': {
        const record = this.#pluginFind(body.id)
        const values = body.values
        if (typeof values !== 'object' || values === null || Array.isArray(values)) throw new ControlPlaneError('invalid_plugin', 'Plugin storage update is malformed')
        const count = Object.keys(values).length
        if (count > 64 || (count === 0 && body.clear !== true)) throw new ControlPlaneError('invalid_plugin', 'Plugin values must list at most 64 keys, and at least one unless clearing')
        return { values: this.#pluginStore(record, values as Record<string, unknown>, body.clear === true) }
      }
      case 'plugins.secret.set': {
        const record = this.#pluginFind(body.id)
        const key = typeof body.key === 'string' ? body.key : ''
        if (!record.package.settings?.some((setting) => setting.key === key && setting.type === 'secret')) throw new ControlPlaneError('invalid_plugin', 'Plugin has no such secret setting')
        const secrets = { ...record.secrets }
        if (typeof body.value === 'string' && body.value) secrets[key] = body.value
        else delete secrets[key]
        record.secrets = secrets
        this.#pluginChanged()
        return { secrets: Object.keys(secrets).sort() }
      }
      case 'plugins.fetch': {
        this.#pluginFind(body.id)
        // Nothing leaves a dev browser: every request gets the same canned answer.
        return { status: 200, headers: { 'content-type': 'application/json' }, body: JSON.stringify({ fixture: true, method: body.method ?? 'GET', url: body.url }), redirected: false }
      }
      case 'plugins.download': {
        // Nothing leaves a dev browser: a link ending in an example's file
        // name (…/dusk-theme.json) answers that example.
        // Loaded on demand: the examples bundle docs folders through Vite, which
        // the headless plugin runner (plain Node) cannot do.
        const url = typeof body.url === 'string' ? body.url.trim() : ''
        return import('../features/plugins/domain/examples').then(({ EXAMPLE_PLUGINS }) => {
          const example = EXAMPLE_PLUGINS.find((entry) => url.endsWith(`/${entry.file}`))
          if (!example) throw new ControlPlaneError('download_failed', 'Nothing was found at that link.')
          return { package: example.package, source: url }
        })
      }
      case 'plugins.dev.link': throw new ControlPlaneError('plugin_dev_problem', 'Loading a plugin from a folder needs the desktop app.')
      case 'plugins.dev.unlink': throw new ControlPlaneError('plugin_not_linked', 'Plugin is not loaded from a folder')
      default:
        if (method.startsWith('plugins.collection.')) return this.#collectionAnswer(method, body)
        throw new Error(`fixture session has no answer for ${method}`)
    }
  }
  // ── End plugins ──────────────────────────────────────────────────────────

  #answer(method: string, payload?: unknown): unknown {
    const body = (payload ?? {}) as Record<string, never>
    if (method.startsWith('plugins.')) return this.#pluginAnswer(method, body)
    if (method.startsWith('rules.')) return this.#rules.answer(method, body)
    switch (method) {
      case 'appearance.get': return { ...this.#appearance }
      case 'appearance.update': {
        this.#appearance = { ...body }
        try { window.localStorage.setItem('switchboard.fixture.appearance', JSON.stringify(this.#appearance)) } catch { /* dev only */ }
        this.#emit('appearance.changed', { ...this.#appearance })
        return { ...this.#appearance }
      }
      // No relay traffic in the fixture, so hooks are accepted and never called.
      case 'relayhooks.register': return { registered: true }
      case 'relayhooks.unregister': return { removed: 1 }
      case 'relayhooks.reset': return { removed: 0 }
      case 'relayhooks.respond': return { accepted: false }
      case 'relay.status': return { state: 'live', address: 'http://127.0.0.1:8787', port: 8787 }
      case 'updates.status': return { ...updateFeed }
      case 'updates.check': return (async () => {
        // Late enough for the button to show its own in-flight state. A
        // completed check is announced as an event too — the same channel the
        // refresher's automatic passes arrive on — so the pill, the dialog and
        // the Settings panel stay one truth, not three.
        await new Promise((resolve) => setTimeout(resolve, 600))
        const answer = { ...updateFeed, checkedAt: new Date().toISOString() }
        this.#emit('updates.stateChanged', answer)
        return answer
      })()
      case 'updates.install': {
        // The fixture reports progress the way the sidecar does, then parks a
        // file that names itself: no real bytes ever move in the dev shell.
        return (async () => {
          await new Promise((resolve) => setTimeout(resolve, 400))
          this.#emit('updates.installProgress', { phase: 'downloading', received: 0, total: 11_865_030, percent: 0 })
          await new Promise((resolve) => setTimeout(resolve, 350))
          this.#emit('updates.installProgress', { phase: 'downloading', received: 5_932_515, total: 11_865_030, percent: 50 })
          await new Promise((resolve) => setTimeout(resolve, 350))
          this.#emit('updates.installProgress', { phase: 'downloading', received: 11_865_030, total: 11_865_030, percent: 100 })
          await new Promise((resolve) => setTimeout(resolve, 250))
          this.#emit('updates.installProgress', { phase: 'ready', received: 11_865_030, total: 11_865_030, percent: 100 })
          return { path: 'C:\\Users\\dev\\AppData\\Local\\ProviderSwitchboard\\update\\Luxury-Switchboard-1.0.99-windows-x64-setup.exe', version: '1.0.99' }
        })()
      }
      case 'relay.start': case 'relay.stop': return { state: 'live', address: 'http://127.0.0.1:8787', port: 8787 }
      case 'providers.list': return { activeId: this.#activeProvider, providers }
      case 'providers.activate': {
        const provider = providers.find((candidate) => candidate.id === (body as { id?: unknown }).id)
        if (!provider) throw new ControlPlaneError('provider_not_found', 'Provider not found')
        this.#activeProvider = provider.id
        this.#emit('providers.changed', {})
        return provider
      }
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
         const profile = String(body.profile ?? 'short')
        const fullProfile = profile === 'full-128k'
         const models = Array.isArray(body.models) ? (body.models as readonly string[]) : []
        // Per-provider seed: identical rows across providers would read as
        // copied data, and the page exists to compare them.
        const seed = providerId.split('').reduce((sum, char) => sum + char.charCodeAt(0), 0)
        models.forEach((model, index) => {
          const timer = window.setTimeout(() => {
            this.#runTimers.delete(timer)
            const failed = model.includes('flash') && providerId === 'sigma-llm'
             const ttft = (fullProfile ? 1_200 : 380) + ((index * 211 + seed * 7) % (fullProfile ? 3_600 : 2_600))
            const total = ttft + (fullProfile ? 2_400 : 700) + ((index * 503 + seed * 31) % (fullProfile ? 6_000 : 4_000))
            // The counting task: ~120–170 tokens, rate measured after the first.
            const tokens = 119 + ((index * 17 + seed) % 52)
            this.#emit('models.tested', {
              runId, providerId, model,
              state: failed ? 'unavailable' : 'available',
              status: failed ? 429 : 200,
              latencyMs: total,
              ttftMs: failed ? 0 : ttft,
              outputTokens: failed ? 0 : tokens,
              outputTps: failed ? 0 : ((tokens - 1) * 1_000) / (total - ttft),
              ...(failed ? { errorCode: 'rate_limited' } : {}),
            })
          }, (fullProfile ? 480 : 320) * (index + 1))
          this.#runTimers.add(timer)
        })
        const pending = new Promise<{ tested: number }>((resolve) => {
          window.setTimeout(() => resolve({ tested: models.length }), (fullProfile ? 480 : 320) * (models.length + 1))
        })
        return pending
      }
      case 'keys.list': return { keys: [
        { id: 'k1', providerId: body.providerId, label: 'primary', priority: 0, rpm: 40, pinned: false, proxyConfigured: false, cooldownMs: 0, blockedModels: 0, retries429: 3, startsInWindow: 11, authStreak: 0, lastOutcome: 'success' },
        { id: 'k2', providerId: body.providerId, label: 'backup-2', priority: 1, rpm: 40, pinned: false, proxyConfigured: true, cooldownMs: 42_000, blockedModels: 0, retries429: 14, startsInWindow: 4, authStreak: 1, lastOutcome: 'rate-limited' },
        { id: 'k3', providerId: body.providerId, label: 'old-reseller', priority: 2, rpm: 0, pinned: false, proxyConfigured: false, cooldownMs: 0, blockedModels: 2, retries429: 91, startsInWindow: 0, authStreak: 5, lastOutcome: 'authentication' },
        { id: 'k4', providerId: body.providerId, label: 'pinned-ip', priority: 3, rpm: 20, pinned: true, proxyConfigured: false, cooldownMs: 0, blockedModels: 0, retries429: 0, startsInWindow: 2, authStreak: 0, lastOutcome: 'success' },
      ], available: 4 }
      case 'keys.check': return new Promise((resolve) => window.setTimeout(() => resolve({ total: 3, checked: 2, answered: 2, accepted: 1, rejected: 1, inconclusive: 0, skipped: 1, reachable: true }), 900))
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
      case 'codex.login.start': {
        this.#codexBeginLogin()
        return { authorizeUrl: 'https://auth.codex.example/oauth/authorize?fixture=1' }
      }
      case 'codex.login.device.start': {
        // The device flow shares the login curve but never owns an authorize
        // URL: the user completes it on another machine's browser, so there is
        // nothing to re-open locally while the poll runs.
        this.#codexBeginLogin()
        return { userCode: 'WDJB-MJCD', verificationUrl: 'https://auth.codex.example/device?fixture=1', pollIntervalSeconds: 1 }
      }
      case 'codex.import.json': {
        // The real parser refuses an empty text before anything moves.
        const text = String(body.text ?? '')
        if (text.trim() === '') throw new Error('codex import payload could not be parsed')
        // A stale pair whose refresh the server refused: the wrapper wording
        // is the exact text the frontend maps to its actionable copy.
        if (text.includes('rejected')) {
          throw new Error('codex import failed: none of the 1 credentials produced a codex session: codex refresh token was rejected: codex oauth refresh failed: refresh_token_reused')
        }
        return this.#codexImportResult(text.includes('plus') ? 'Plus' : 'Pro')
      }
      case 'codex.import.files': {
        const paths = Array.isArray(body.paths) ? (body.paths as readonly string[]) : []
        if (paths.length === 0) throw new Error('none of the selected files held codex credentials')
        const importedFrom = paths[paths.length - 1]!.split(/[\\/]/).pop() ?? 'auth.json'
        return { ...this.#codexImportResult('Plus'), importedFrom }
      }
      case 'codex.login.status': {
        const phase = this.#codexPhase
        const error = this.#codexError
        // Terminal phases are reported once, then the login goes back to
        // idle: the account state carries the result from there on.
        if (phase === 'success' || phase === 'error') {
          this.#codexPhase = 'idle'
          this.#codexError = ''
        }
        return error ? { phase, error } : { phase }
      }
      case 'codex.login.cancel': {
        this.#codexCancel()
        return {}
      }
      case 'codex.status': return this.#codexStatus()
      case 'codex.quota': {
        // The real sidecar answers with the account it was asked about; a
        // signed-out session is a refusal in the result, not a broken
        // command — exactly like the backend's probe.
        const accountId = typeof body.accountId === 'string' ? body.accountId : ''
        const account = this.#codexAccounts.find((entry) => entry.accountId === accountId)
        if (account === undefined || account.state !== 'signed_in') return { accountId, error: 'codex is not signed in' }
        return { accountId, quota: this.#codexQuota(account) }
      }
      case 'codex.quota.all': {
        // The fleet probe answers one row per account the single probe
        // answers, so a reauth-needed account shows up as its own refusal
        // instead of silently vanishing from the list.
        const rows = this.#codexAccounts.map((account) =>
          account.state === 'signed_in'
            ? { accountId: account.accountId, quota: this.#codexQuota(account) }
            : { accountId: account.accountId, error: 'codex is not signed in' })
        return { results: rows }
      }
      case 'codex.wake': {
        // The wake reports what the backend actually observed per account: a
        // live session stays live, a broken one stays broken with its reason.
        const asked = typeof body.accountId === 'string' ? body.accountId : ''
        const rows = this.#codexAccounts
          .filter((account) => asked === '' || account.accountId === asked)
          .map((account) => account.state === 'signed_in'
            ? { accountId: account.accountId, state: 'signed_in' }
            : { accountId: account.accountId, state: account.state, error: 'the stored session expired' })
        return { results: rows }
      }
      case 'codex.logout': {
        // A named account drops only itself; the cascade — or an empty
        // payload, which the backend reads the same way — clears every
        // account at once.
        this.#codexCancel()
        const accountId = typeof body.accountId === 'string' ? body.accountId : ''
        if (accountId === '') this.#codexAccounts = []
        else this.#codexAccounts = this.#codexAccounts.filter((entry) => entry.accountId !== accountId)
        this.#emit('codex.changed', {})
        return {}
      }
      // A command the fixture never stubbed must fail loudly in the dev
      // console, not silently succeed with an empty object.
      default: throw new Error(`fixture session has no answer for ${method}`)
    }
  }
}
