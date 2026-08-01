// @vitest-environment jsdom

import { fireEvent, render, screen } from '@testing-library/react'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import type { ControlPlaneSession, EventListener } from './platform/stdio/session'

const createSession = vi.hoisted(() => vi.fn())
vi.mock('./platform/stdio/create-session', () => ({ createControlPlaneSession: createSession }))

import App from './App'

beforeAll(async () => {
  await Promise.all([
    import('./features/overview/ui/OverviewPage'),
    import('./features/activity/ui/ActivityPage'),
    import('./features/providers/ui/ProvidersPage'),
    import('./features/api-keys/ui/ApiKeysPage'),
    import('./features/model-routes/ui/ModelRoutesPage'),
    import('./features/tunnel/ui/TunnelPage'),
    import('./features/clients/ui/ClientsPage'),
    import('./features/statistics/ui/StatisticsPage'),
    import('./features/shared-control/ui/SharedControlPage'),
    import('./features/settings/ui/SettingsPage'),
  ])
}, 20_000)

const provider = { id: 'local', name: 'Local', baseUrl: 'http://127.0.0.1:8799', authMode: 'passthrough', authHeader: '', dialect: 'auto', modelsPath: '/v1/models', rpm: 0, cacheTtl: '0s', enabled: true, keyConfigured: false, keyCount: 0, builtin: true }
const settings = { listenerPort: 8798, maxRequestMiB: 64, headerTimeoutSeconds: 45, streamIdleSeconds: 60, retryBaseMilliseconds: 500, retryMaxSeconds: 30, permanentAttempts: 2, maxQueued: 10_000, activityCapacity: 2_000, historyRetentionDays: 30, tunnelRetentionHours: 72 }

function fakeSession(relaySnapshot: unknown = { state: 'live', address: 'http://127.0.0.1:8798', port: 8798 }, settingsSnapshot = settings, overrides: Record<string, unknown> = {}): ControlPlaneSession {
  return {
    async start() {},
    async stop() {},
    async call<T>(method: string): Promise<T> {
      const responses: Record<string, unknown> = {
        'relay.status': relaySnapshot,
        'providers.list': { activeId: 'local', providers: [provider] },
        'activity.list': { requests: [] },
        'activity.summary': { requests: 0, active: 0, queued: 0, successRate: 0, p95Ms: 0, rpm: 0 },
        'settings.get': settingsSnapshot,
        'tunnel.get': { state: 'stopped', port: 8797, address: '', rpmPerIp: 0, contextLimitKiB: 0, brandResponse: 'Luxury Private', publisherProfile: '', tokenConfigured: true },
        'clients.list': { clients: [] },
        'shared.list': { available: false, revision: 0, tunnels: [], error: 'Unavailable' },
        'keys.list': { keys: [] },
        'routes.list': { routes: [] },
        'models.discover': { models: [] },
        'history.stats': { requests: 0, completed: 0, failed: 0, cancelled: 0, retries: 0, inputTokens: 0, outputTokens: 0, cachedTokens: 0, reasoningTokens: 0, processedTokens: 0, nonCachedTokens: 0, p95Ms: 0, tokensPerSecond: 0 },
        'history.recent': { requests: [] },
        ...overrides,
      }
      if (!(method in responses)) throw new Error(`Unexpected method: ${method}`)
      return responses[method] as T
    },
    subscribe<T>(_topic: string, _listener: EventListener<T>) { return () => undefined },
  }
}

async function renderPages(pages: ReadonlyArray<readonly [string, string]>) {
  window.location.hash = ''
  createSession.mockResolvedValue(fakeSession())
  const view = render(<App />)
  for (const [navigation, heading] of pages) {
    fireEvent.click(await screen.findByRole('button', { name: navigation }))
    expect(await screen.findByRole('heading', { name: heading, level: 1 })).toBeTruthy()
  }
  view.unmount()
}

describe('App navigation', () => {
  it('renders the primary workspaces', async () => {
    await renderPages([
      ['Overview', 'Overview'],
      ['Live Activity', 'Live Activity'],
      ['Providers', 'Providers'],
      ['API Keys', 'API Keys'],
      ['Model Routes', 'Model Routes'],
    ])
  })

  it('renders the operational workspaces', async () => {
    await renderPages([
      ['Tunnel', 'Tunnel'],
      ['Clients', 'Tunnel Clients'],
      ['Statistics', 'Statistics'],
      ['Shared Control', 'Shared Control'],
      ['Settings', 'Settings'],
    ])
  })

  it('shows the configured listener instead of a hardcoded stopped address', async () => {
    createSession.mockResolvedValue(fakeSession({ state: 'stopped', address: '', port: 0 }, { ...settings, listenerPort: 19001 }))
    const view = render(<App />)
    expect(await screen.findByText('127.0.0.1:19001')).toBeTruthy()
    view.unmount()
  })

  it('does not offer a no-op move above the pinned environment key', async () => {
    const keys = [
      { id: 'pinned', providerId: 'local', label: 'Environment', priority: 0, rpm: 30, pinned: true, proxyConfigured: false, cooldownMs: 0, blockedModels: 0, retries429: 0, startsInWindow: 0 },
      { id: 'user', providerId: 'local', label: 'User', priority: 1, rpm: 120, pinned: false, proxyConfigured: false, cooldownMs: 0, blockedModels: 0, retries429: 0, startsInWindow: 0 },
    ]
    createSession.mockResolvedValue(fakeSession(undefined, settings, { 'keys.list': { keys } }))
    const view = render(<App />)
    fireEvent.click(await screen.findByRole('button', { name: 'API Keys' }))
    expect((await screen.findByRole('button', { name: 'Move User up' }) as HTMLButtonElement).disabled).toBe(true)
    view.unmount()
  })
})
