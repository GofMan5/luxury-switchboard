import { PROTOCOL_VERSION, type EventFrame } from '../../shared/contracts/protocol'
import type { ControlPlaneSession, EventListener } from './session'

const initialProviders = [
  {
    id: 'local', name: 'Local', baseUrl: 'http://127.0.0.1:8799',
    authMode: 'passthrough', authHeader: '', dialect: 'auto', modelsPath: '/v1/models', rpm: 0, cacheTtl: '0s', enabled: true,
    keyConfigured: false, keyCount: 0, builtin: true,
  },
  {
    id: 'echo', name: 'EchoGate', baseUrl: 'https://api.echogate.one/v1',
    authMode: 'auto', authHeader: '', dialect: 'auto', modelsPath: '/v1/models', rpm: 120, cacheTtl: '1h0m0s', enabled: true,
    keyConfigured: true, keyCount: 1, builtin: true,
  },
]

interface BrowserRoute {
  target: 'relay' | 'tunnel'
  publicModel: string
  upstreamModel: string
  providerId: string
  contextLimitKiB: number
  enabled: boolean
}

export class BrowserSession implements ControlPlaneSession {
  #activeId = 'echo'
  #relay = { state: 'live', address: 'http://127.0.0.1:8798', port: 8798 }
  #listeners = new Map<string, Set<EventListener>>()
  #sequence = 0
  #keys = browserKeys()
  #providers = initialProviders.map((provider) => ({ ...provider }))
  #settings = browserSettings()
  #routes: BrowserRoute[] = []
  #clients = browserClients()
  #clientEvents = browserClientEvents()
  #shared = { available: true, revision: 7, error: '', tunnels: [{ position: 0, name: 'Ваш коннект', state: 'running' }, { position: 1, name: 'Tunnel 1', state: 'paused' }] }
  #tunnel = { state: 'stopped', port: 8797, address: '', rpmPerIp: 0, contextLimitKiB: 0, brandResponse: 'Luxury Private лучший приватный софт для абузов - @Luxuryprivate_bot', publisherProfile: '', tokenConfigured: true }
  #tunnelToken = 'browser-fixture-public-token-000000000000000000'

  async start(): Promise<void> {}

  async call<T>(method: string, payload?: unknown): Promise<T> {
    const result = this.#handle(method, payload)
    return result as T
  }

  subscribe<T>(topic: string, listener: EventListener<T>): () => void {
    const listeners = this.#listeners.get(topic) ?? new Set<EventListener>()
    listeners.add(listener as EventListener)
    this.#listeners.set(topic, listeners)
    return () => listeners.delete(listener as EventListener)
  }

  async stop(): Promise<void> {}

  #handle(method: string, payload?: unknown): unknown {
    switch (method) {
      case 'system.handshake':
        return { protocol: 1, appVersion: '0.2.0', capabilities: [] }
      case 'relay.status':
        return this.#relay
      case 'relay.start':
        this.#relay = { state: 'live', address: 'http://127.0.0.1:8798', port: 8798 }
        this.#emit('relay.changed', this.#relay)
        return this.#relay
      case 'relay.stop':
        this.#relay = { state: 'stopped', address: '', port: 0 }
        this.#emit('relay.changed', this.#relay)
        return this.#relay
      case 'providers.list':
        return { activeId: this.#activeId, providers: this.#providers }
      case 'providers.activate': {
        const id = (payload as { id?: string } | undefined)?.id
        if (this.#providers.some((provider) => provider.id === id)) this.#activeId = id ?? this.#activeId
        this.#emit('providers.changed', { activeId: this.#activeId })
        return this.#providers.find((provider) => provider.id === this.#activeId)
      }
      case 'providers.add': {
        const value = payload as { name: string; baseUrl: string; authMode: string; authHeader: string; dialect: string; modelsPath: string; rpm: number; cache1h: boolean; enabled: boolean }
        const provider = {
          id: `provider_demo_${this.#providers.length + 1}`,
          name: value.name,
          baseUrl: value.baseUrl,
          authMode: value.authMode,
          authHeader: value.authHeader,
          dialect: value.dialect,
          modelsPath: value.modelsPath,
          rpm: value.rpm,
          cacheTtl: value.cache1h ? '1h0m0s' : '0s',
          enabled: value.enabled,
          keyConfigured: false,
          keyCount: 0,
          builtin: false,
        }
        this.#providers = [...this.#providers, provider]
        this.#emit('providers.changed', { providerId: provider.id })
        return provider
      }
      case 'providers.update': {
        const value = payload as { id: string; name: string; baseUrl: string; authMode: string; authHeader: string; dialect: string; modelsPath: string; rpm: number; cache1h: boolean; enabled: boolean }
        let updated = this.#providers.find((provider) => provider.id === value.id)
        this.#providers = this.#providers.map((provider) => {
          if (provider.id !== value.id) return provider
          updated = {
            ...provider,
            name: value.name,
            baseUrl: value.baseUrl,
            authMode: value.authMode,
            authHeader: value.authHeader,
            dialect: value.dialect,
            modelsPath: value.modelsPath,
            rpm: value.rpm,
            cacheTtl: value.cache1h ? '1h0m0s' : '0s',
            enabled: value.enabled,
          }
          return updated
        })
        this.#emit('providers.changed', { providerId: value.id })
        return updated
      }
      case 'providers.delete': {
        const id = (payload as { id: string }).id
        this.#providers = this.#providers.filter((provider) => provider.id !== id)
        this.#emit('providers.changed', { providerId: id })
        return { deleted: true }
      }
      case 'activity.list':
        return { requests: browserActivity() }
      case 'activity.summary':
        return {
          requests: 18,
          active: 3,
          queued: 2,
          successRate: 99.2,
          p95Ms: 1840,
          rpm: 42.8,
        }
      case 'keys.list': {
        const providerId = (payload as { providerId?: string } | undefined)?.providerId
        return { keys: this.#keys.filter((key) => key.providerId === providerId) }
      }
      case 'keys.add': {
        const value = payload as { providerId: string; label: string; rpm: number; proxyUrl?: string }
        const key = {
          id: `key_demo_${this.#keys.length + 1}`,
          providerId: value.providerId,
          label: value.label,
          priority: this.#keys.filter((item) => item.providerId === value.providerId).length,
          rpm: value.rpm,
          pinned: false,
          proxyConfigured: Boolean(value.proxyUrl),
          cooldownMs: 0,
          blockedModels: 0,
          retries429: 0,
          startsInWindow: 0,
        }
        this.#keys = [...this.#keys, key]
        this.#emit('keys.changed', { providerId: value.providerId })
        return key
      }
      case 'keys.update': {
        const value = payload as { providerId: string; keyId: string; label: string; rpm: number; proxyUrl?: string }
        let updated = this.#keys.find((key) => key.id === value.keyId)
        this.#keys = this.#keys.map((key) => {
          if (key.id !== value.keyId) return key
          updated = {
            ...key,
            label: value.label,
            rpm: value.rpm,
            ...(value.proxyUrl === undefined ? {} : { proxyConfigured: Boolean(value.proxyUrl) }),
          }
          return updated
        })
        this.#emit('keys.changed', { providerId: value.providerId })
        return updated
      }
      case 'keys.remove': {
        const value = payload as { providerId: string; keyId: string }
        this.#keys = this.#keys.filter((key) => key.id !== value.keyId)
        this.#emit('keys.changed', { providerId: value.providerId })
        return { removed: true }
      }
      case 'keys.move': {
        const value = payload as { providerId: string; keyId: string; direction: -1 | 1 }
        const group = this.#keys.filter((key) => key.providerId === value.providerId)
        const position = group.findIndex((key) => key.id === value.keyId)
        const target = position + value.direction
        if (position >= 0 && target >= 0 && target < group.length) {
          const moved = group[position]
          group[position] = group[target]
          group[target] = moved
          const order = new Map(group.map((key, index) => [key.id, index]))
          this.#keys = this.#keys.map((key) => ({ ...key, priority: order.get(key.id) ?? key.priority }))
        }
        this.#emit('keys.changed', { providerId: value.providerId })
        return { moved: true }
      }
      case 'keys.reset': {
        const value = payload as { providerId: string; keyId: string }
        this.#keys = this.#keys.map((key) => key.id === value.keyId ? { ...key, cooldownMs: 0, blockedModels: 0 } : key)
        this.#emit('keys.changed', { providerId: value.providerId })
        return { reset: true }
      }
      case 'settings.get':
        return this.#settings
      case 'settings.update': {
        this.#settings = { ...(payload as ReturnType<typeof browserSettings>) }
        const result = { settings: this.#settings, restartRequired: true }
        this.#emit('settings.changed', result)
        return result
      }
      case 'history.recent':
        return { requests: browserActivity().filter((request) => ['completed', 'failed', 'cancelled'].includes(request.state)) }
      case 'history.stats':
        return {
          requests: 1248, completed: 1192, failed: 41, cancelled: 15, retries: 86,
          inputTokens: 8_420_000, outputTokens: 620_000, cachedTokens: 3_180_000,
          reasoningTokens: 142_000, processedTokens: 9_040_000,
          nonCachedTokens: 5_860_000, p95Ms: 1840, tokensPerSecond: 48.6,
        }
      case 'routes.list': {
        const target = (payload as { target: 'relay' | 'tunnel' }).target
        return { routes: this.#routes.filter(route => route.target === target) }
      }
      case 'routes.upsert': {
        const route = payload as BrowserRoute
        this.#routes = [...this.#routes.filter(item => item.target !== route.target || item.publicModel !== route.publicModel), route]
        this.#emit('routes.changed', { target: route.target })
        return route
      }
      case 'routes.upsertMany': {
        const routes = (payload as { routes: readonly BrowserRoute[] }).routes
        for (const route of routes) {
          this.#routes = [...this.#routes.filter(item => item.target !== route.target || item.publicModel !== route.publicModel), route]
        }
        if (routes[0]) this.#emit('routes.changed', { target: routes[0].target })
        return { saved: routes.length }
      }
      case 'routes.delete': {
        const value = payload as { target: 'relay' | 'tunnel'; publicModel: string }
        this.#routes = this.#routes.filter(route => route.target !== value.target || route.publicModel !== value.publicModel)
        this.#emit('routes.changed', { target: value.target })
        return { deleted: true }
      }
      case 'models.discover':
        return { providerId: (payload as { providerId: string }).providerId, models: browserModels() }
      case 'models.test': {
        const value = payload as { providerId: string; models: readonly string[] }
        value.models.forEach((model, index) => this.#emit('models.tested', {
          providerId: value.providerId,
          model,
          state: index % 7 === 6 ? 'unavailable' : 'available',
          status: index % 7 === 6 ? 503 : 200,
          latencyMs: 420 + index * 83,
          ...(index % 7 === 6 ? { errorCode: 'provider_unavailable' } : {}),
        }))
        return { tested: value.models.length }
      }
      case 'tunnel.get': return this.#tunnel
      case 'tunnel.configure': {
        const value = payload as { port: number; rpmPerIp: number; contextLimitKiB: number; brandResponse: string; publisherProfile: string }
        this.#tunnel = { ...this.#tunnel, ...value }
        this.#emit('tunnel.changed', this.#tunnel)
        return this.#tunnel
      }
      case 'tunnel.start':
        this.#tunnel = { ...this.#tunnel, state: 'online', address: `http://127.0.0.1:${this.#tunnel.port}/v1` }
        this.#emit('tunnel.changed', this.#tunnel); return this.#tunnel
      case 'tunnel.stop':
        this.#tunnel = { ...this.#tunnel, state: 'stopped', address: '' }
        this.#emit('tunnel.changed', this.#tunnel); return this.#tunnel
      case 'tunnel.rotate':
        this.#tunnelToken = `browser-rotated-${Date.now()}-000000000000000000`
        return { token: this.#tunnelToken }
      case 'tunnel.reveal': return { token: this.#tunnelToken }
      case 'clients.list': return { clients: this.#clients }
      case 'clients.events': {
        const ip = (payload as { ip?: string } | undefined)?.ip
        return { events: this.#clientEvents.filter((event) => event.ip === ip) }
      }
      case 'shared.list': return this.#shared
      case 'shared.control': {
        const value = payload as { position: number; revision: number; action: 'pause' | 'resume' | 'stop' }
        if (value.revision !== this.#shared.revision) throw new Error('stale shared state')
        const desired = value.action === 'pause' ? 'paused' : value.action === 'resume' ? 'running' : 'stopped'
        this.#shared = {
          ...this.#shared,
          revision: this.#shared.revision + 1,
          tunnels: this.#shared.tunnels.map((tunnel) => tunnel.position === value.position ? { ...tunnel, state: desired } : tunnel),
        }
        return this.#shared
      }
      default:
        throw new Error(`Unsupported browser command: ${method}`)
    }
  }

  #emit(topic: string, payload: unknown): void {
    const event: EventFrame = {
      v: PROTOCOL_VERSION,
      type: 'event',
      topic,
      seq: ++this.#sequence,
      payload,
    }
    for (const listener of this.#listeners.get(topic) ?? []) listener(event)
  }
}

function browserActivity() {
  const rows = [
    ['active', 'gpt-5.6-sol', 'EchoGate', 0, 1120, 0],
    ['completed', 'claude-opus-5', 'EchoGate', 200, 2480, 0],
    ['retrying', 'kimi-k3', 'EchoGate', 429, 0, 2],
    ['completed', 'gemini-3.6-flash', 'Local', 200, 684, 0],
    ['failed', 'mistral-large', 'EchoGate', 422, 3010, 1],
    ['completed', 'qwen-3.7-plus', 'EchoGate', 200, 1450, 0],
    ['cancelled', 'claude-sonnet-5', 'Local', 0, 892, 0],
    ['completed', 'gpt-5.5', 'EchoGate', 200, 2110, 0],
  ] as const
  return rows.map(([state, model, providerName, status, latencyMs, retries], index) => ({
    id: `req_demo_${index}`,
    startedAt: new Date(Date.now() - index * 4_000).toISOString(),
    updatedAt: new Date(Date.now() - index * 4_000 + latencyMs).toISOString(),
    state,
    model,
    providerId: providerName === 'Local' ? 'local' : 'echo',
    providerName,
    method: 'POST',
    path: '/v1/responses',
    ...(status ? { status } : {}),
    queueMs: state === 'retrying' ? 340 : 0,
    latencyMs,
    retries,
    bytesIn: 18_240 + index * 1_024,
    bytesOut: state === 'completed' ? 1_024 + index * 128 : 0,
    inputTokens: 1200 + index * 200,
    outputTokens: state === 'completed' ? 180 + index * 20 : 0,
    cachedTokens: 400 + index * 50,
    reasoningTokens: state === 'completed' ? 40 + index * 5 : 0,
    totalTokens: state === 'completed' ? 1380 + index * 220 : 1200 + index * 200,
    contextTokens: 1200 + index * 200,
    generationMs: latencyMs,
    tokensPerSecond: latencyMs > 0 ? 42.5 : 0,
  }))
}

function browserKeys() {
  return [
    {
      id: 'key_environment', providerId: 'echo', label: 'Environment key',
      priority: 0, rpm: 30, pinned: true, proxyConfigured: false,
      cooldownMs: 0, blockedModels: 0, retries429: 2, startsInWindow: 18,
    },
    {
      id: 'key_shared_pro', providerId: 'echo', label: 'Shared Pro',
      priority: 1, rpm: 120, pinned: false, proxyConfigured: true,
      cooldownMs: 0, blockedModels: 0, retries429: 0, startsInWindow: 42,
    },
  ]
}

function browserSettings() {
  return {
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
  }
}

function browserClients() {
  const now = Date.now()
  return [
    { ip: '198.51.100.24', actualRpm: 18, count: 284, active: 2, queued: 0, lastSeen: new Date(now - 1_200).toISOString(), state: 'active' },
    { ip: '203.0.113.17', actualRpm: 7, count: 91, active: 0, queued: 1, lastSeen: new Date(now - 18_000).toISOString(), state: 'idle' },
    { ip: '192.0.2.42', actualRpm: 3, count: 48, active: 0, queued: 0, lastSeen: new Date(now - 74_000).toISOString(), state: 'idle' },
  ]
}

function browserClientEvents() {
  const clients = ['198.51.100.24', '203.0.113.17', '192.0.2.42']
  return Array.from({ length: 12 }, (_, index) => ({
    id: `tun_demo_${index}`,
    ip: clients[index % clients.length],
    time: new Date(Date.now() - index * 9_000).toISOString(),
    state: index === 7 ? 'error' : 'completed',
    method: 'POST',
    path: index % 2 ? '/v1/chat/completions' : '/v1/responses',
    model: index % 3 ? 'gpt-premium' : 'claude-premium',
    status: index === 7 ? 502 : 200,
    latencyMs: 840 + index * 115,
    bytesIn: 4_096 + index * 512,
    bytesOut: index === 7 ? 0 : 1_024 + index * 96,
    ...(index === 7 ? { errorCode: 'upstream_rejected' } : {}),
  }))
}

function browserModels() {
  return [
    'claude-opus-5[1m]', 'claude-sonnet-5', 'gemini-3.6-flash', 'gpt-5.6-sol',
    'gpt-5.6-terra', 'gpt-image-2', 'kimi-k3', 'mistral-large', 'qwen-3.7-plus',
  ]
}
