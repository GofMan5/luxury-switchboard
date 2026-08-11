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

const provider = { id: 'local', name: 'Local', baseUrl: 'http://127.0.0.1:8799', authMode: 'passthrough', authHeader: '', dialect: 'auto', modelsPath: '/v1/models', imageCompat: false, rpm: 0, cacheTtl: '0s', enabled: true, keyConfigured: false, keyCount: 0, builtin: true }
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
    appVersion: '9.9.9',
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

  it('shows the version reported by the control plane, never a hardcoded one', async () => {
    createSession.mockResolvedValue(fakeSession())
    const view = render(<App />)
    expect(await screen.findByText('v9.9.9')).toBeTruthy()
    view.unmount()
  })

  it('shows the configured listener instead of a hardcoded stopped address', async () => {
    createSession.mockResolvedValue(fakeSession({ state: 'stopped', address: '', port: 0 }, { ...settings, listenerPort: 19001 }))
    const view = render(<App />)
    expect(await screen.findByText('127.0.0.1:19001')).toBeTruthy()
    view.unmount()
  })

  it('does not offer a no-op move above a pinned managed key', async () => {
    const keys = [
      { id: 'pinned', providerId: 'local', label: 'Managed', priority: 0, rpm: 30, pinned: true, proxyConfigured: false, cooldownMs: 0, blockedModels: 0, retries429: 0, startsInWindow: 0 },
      { id: 'user', providerId: 'local', label: 'User', priority: 1, rpm: 120, pinned: false, proxyConfigured: false, cooldownMs: 0, blockedModels: 0, retries429: 0, startsInWindow: 0 },
    ]
    createSession.mockResolvedValue(fakeSession(undefined, settings, { 'keys.list': { keys } }))
    const view = render(<App />)
    fireEvent.click(await screen.findByRole('button', { name: 'API Keys' }))
    expect((await screen.findByRole('button', { name: 'Move User up' }) as HTMLButtonElement).disabled).toBe(true)
    view.unmount()
  })

  it('renders a large model catalog in bounded chunks', async () => {
    const models = Array.from({ length: 5_000 }, (_, index) => `model-${String(index).padStart(4, '0')}`)
    createSession.mockResolvedValue(fakeSession(undefined, settings, { 'models.discover': { models } }))
    const view = render(<App />)
    fireEvent.click(await screen.findByRole('button', { name: 'Model Routes' }))

    expect(await screen.findByText('Showing 180 of 5000 models')).toBeTruthy()
    expect(document.querySelectorAll('input[type="checkbox"]')).toHaveLength(180)
    fireEvent.click(screen.getByRole('button', { name: 'Show 180 more' }))
    expect(await screen.findByText('Showing 360 of 5000 models')).toBeTruthy()
    expect(document.querySelectorAll('input[type="checkbox"]')).toHaveLength(360)
    view.unmount()
  })

  it('mirrors the published relay routes in the model catalog selection', async () => {
    const routes = [{ target: 'relay', publicModel: 'model-a', upstreamModel: 'model-a', providerId: 'local', contextLimitKiB: 0, enabled: true }]
    createSession.mockResolvedValue(fakeSession(undefined, settings, {
      'models.discover': { models: ['model-a', 'model-b'] },
      'routes.list': { routes },
    }))
    const view = render(<App />)
    fireEvent.click(await screen.findByRole('button', { name: 'Model Routes' }))

    expect(await screen.findByText('2 discovered · 1 on relay · 1 selected')).toBeTruthy()
    expect(screen.getByText('Selection matches the published routes')).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Apply to relay' }) as HTMLButtonElement).disabled).toBe(true)

    const checkboxes = document.querySelectorAll<HTMLInputElement>('.modelList input[type="checkbox"], label input[type="checkbox"]')
    fireEvent.click(checkboxes[1])
    expect(await screen.findByText('+1 new · −0 removed')).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Apply to relay' }) as HTMLButtonElement).disabled).toBe(false)

    fireEvent.click(screen.getByRole('button', { name: 'Not published' }))
    expect(await screen.findByText('Showing 1 of 1 models')).toBeTruthy()
    view.unmount()
  })

  it('offers ban and note controls for a tunnel client', async () => {
    const clients = [{ ip: '203.0.113.7', actualRpm: 0, count: 3, active: 0, queued: 0, refused: 12, lastSeen: '0001-01-01T00:00:00Z', state: 'idle', banned: false, note: 'known tester' }]
    createSession.mockResolvedValue(fakeSession(undefined, settings, { 'clients.list': { clients } }))
    const view = render(<App />)
    fireEvent.click(await screen.findByRole('button', { name: 'Clients' }))

    expect(await screen.findByText('known tester')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Ban 203.0.113.7' })).toBeTruthy()
    expect(screen.queryByText('00:00:00')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Open logs and notes for 203.0.113.7' }))
    expect(await screen.findByRole('dialog', { name: '203.0.113.7' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Ban client' })).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Save note' }) as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getByText('12')).toBeTruthy()
    view.unmount()
  })
})
