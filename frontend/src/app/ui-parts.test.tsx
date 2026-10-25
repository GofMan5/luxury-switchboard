// @vitest-environment jsdom

import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { Gauge } from 'lucide-react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { StdioNotificationsPort } from '../features/notifications/adapters/stdio-notifications-port'
import { NotificationsModel } from '../features/notifications/application/notifications-model'
import { StdioProvidersPort } from '../features/providers/adapters/stdio-providers-port'
import { ProvidersModel } from '../features/providers/application/providers-model'
import { StdioRelayPort } from '../features/relay/adapters/stdio-relay-port'
import { RelayModel } from '../features/relay/application/relay-model'
import { StdioSettingsPort } from '../features/settings/adapters/stdio-settings-port'
import { SettingsModel } from '../features/settings/application/settings-model'
import { StdioUpdatesPort } from '../features/updates/adapters/stdio-updates-port'
import { UpdatesModel } from '../features/updates/application/updates-model'
import type { ControlPlaneSession } from '../platform/stdio/session'
import { ExtensionPoints } from '../shared/contracts/extensions'
import { UI_PARTS } from '../shared/contracts/ui-parts'
import { EmptyState, Metric, MetricStrip, Panel, Pill, Segmented } from '../shared/ui/chrome'
import { AppShell } from './AppShell'
import { ServicesContext, type AppServices } from './services'

// The safe-mode badge only renders in a safe-mode launch, which is read once
// from the URL at import time.
vi.mock('../shared/runtime/safe-mode', () => ({ safeMode: true, restartInSafeMode: vi.fn(), isSafeModeChord: () => false }))

afterEach(() => {
  cleanup()
  // The runner's storage stub may lack methods; the shell guards it the same way.
  try { window.localStorage.removeItem('switchboard.sidebar') } catch { /* nothing persisted */ }
  vi.restoreAllMocks()
})

const PLUGIN = 'plugin:acme.tools'
const NO_QUESTION = { asking: null }

function fakeSession(): ControlPlaneSession {
  const responses: Record<string, unknown> = {
    'relay.status': { state: 'live', address: 'http://127.0.0.1:8798', port: 8798 },
    'providers.list': { activeId: 'local', providers: [] },
    'providers.health': { states: [] },
    'notifications.list': { notifications: [] },
    'updates.status': { current: '9.9.9', latest: '9.9.10', url: 'https://example.invalid/release', newer: true, reachable: true, checkedAt: '2026-10-01T00:00:00Z' },
  }
  return {
    async start() {},
    async stop() {},
    async call<T>(method: string): Promise<T> {
      if (!(method in responses)) throw new Error(`Unexpected method: ${method}`)
      return responses[method] as T
    },
    subscribe() { return () => undefined },
  }
}

async function mountShell(children: React.ReactNode = <PageFixture />) {
  const session = fakeSession()
  const extensions = new ExtensionPoints()
  const notifications = new NotificationsModel(new StdioNotificationsPort(session))
  const updates = new UpdatesModel(new StdioUpdatesPort(session))
  const relay = new RelayModel(new StdioRelayPort(session))
  const services = {
    relay,
    providers: new ProvidersModel(new StdioProvidersPort(session)),
    settings: new SettingsModel(new StdioSettingsPort(session)),
    notifications,
    updates,
    extensions,
    // The shell only reads whether a plugin is asking something.
    plugins: { subscribe: () => () => undefined, snapshot: () => NO_QUESTION },
    appVersion: '9.9.9',
    capabilities: ['plugins.manage'],
  } as unknown as AppServices
  await Promise.all([notifications.connect(), relay.connect()])
  updates.connect()
  const view = render(
    <ServicesContext.Provider value={services}>
      <AppShell route="overview" onNavigate={() => undefined}>{children}</AppShell>
    </ServicesContext.Provider>,
  )
  await screen.findByRole('button', { name: /Update to version 9\.9\.10/ })
  return { view, extensions, notifications }
}

/** Every page building block, as a page composes them. */
function PageFixture() {
  return (
    <Panel title="Fixture" subtitle="Parts" actions={<button type="button">Act</button>}>
      <MetricStrip><Metric label="Requests" value="42" tone="success" /></MetricStrip>
      <Segmented label="Period" value="day" options={[{ id: 'day', label: 'Day' }, { id: 'week', label: 'Week' }]} onChange={() => undefined} />
      <Pill tone="warning">Degraded</Pill>
      <EmptyState icon={Gauge} title="Nothing yet" />
    </Panel>
  )
}

function Thrower(): never {
  throw new Error('fixture render failure')
}

/** Part name → the attributes seen on at least one element of that part. */
function collect(into: Map<string, Set<string>>): void {
  for (const element of document.querySelectorAll<HTMLElement>('[data-ui]')) {
    const name = element.dataset.ui ?? ''
    const seen = into.get(name) ?? new Set<string>()
    for (const attribute of element.getAttributeNames()) seen.add(attribute)
    into.set(name, seen)
  }
}

describe('data-ui parts', () => {
  it('renders exactly the documented catalogue, with the attributes it promises', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => undefined)
    const found = new Map<string, Set<string>>()
    const { extensions, notifications } = await mountShell()
    // Contributions that bring the conditional runtime-bar and rail parts.
    act(() => {
      extensions.contribute(PLUGIN, {
        pages: [{ id: 'acme.tools/board', title: 'Board', icon: 'gauge', badge: { text: '3', tone: 'warning' } }],
        status: ['a', 'b', 'c', 'd', 'e'].map((id) => ({ id: `${PLUGIN}:${id}`, text: `Readout ${id}` })),
      })
      notifications.announce({ kind: 'plugin', severity: 'warning', title: 'Fixture notice', body: 'Safe fixture' })
    })
    collect(found)

    fireEvent.click(screen.getByRole('button', { name: 'Collapse sidebar' }))
    collect(found)

    fireEvent.click(screen.getByRole('button', { name: 'Open command palette' }))
    await screen.findByRole('dialog', { name: 'Command palette' })
    collect(found)
    fireEvent.mouseDown(document.querySelector('[data-ui="palette-scrim"]') as Element)
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Command palette' })).toBeNull())

    fireEvent.click(screen.getByRole('button', { name: /^Notifications/ }))
    await screen.findByRole('dialog', { name: 'Notifications' })
    collect(found)
    cleanup()

    await mountShell(<Thrower />)
    await screen.findByRole('alert')
    collect(found)

    const catalogue = new Map(UI_PARTS.map((part) => [part.name, part]))
    expect(new Set(catalogue.keys()).size).toBe(UI_PARTS.length)
    expect([...found.keys()].sort()).toEqual([...catalogue.keys()].sort())
    for (const part of UI_PARTS) {
      for (const attribute of part.attributes ?? []) expect({ part: part.name, attribute, seen: found.get(part.name)?.has(attribute) }).toEqual({ part: part.name, attribute, seen: true })
    }
    // Mounts the whole shell twice; under a parallel full run that alone can pass 5 s.
  }, 30_000)

  it('names the open page on the document root while the shell is mounted', async () => {
    const { view } = await mountShell()
    expect(document.documentElement.dataset.route).toBe('overview')
    expect(document.querySelector('[data-ui="workspace-content"]')?.getAttribute('data-route')).toBe('overview')
    view.unmount()
    expect(document.documentElement.dataset.route).toBeUndefined()
  })

  it('keeps four readouts inline and lists the rest on a focusable pill', async () => {
    const { extensions } = await mountShell()
    act(() => extensions.contribute(PLUGIN, { status: ['a', 'b', 'c', 'd', 'e', 'f'].map((id) => ({ id: `${PLUGIN}:${id}`, text: `Readout ${id}`, tone: 'info' as const })) }))
    const readouts = document.querySelectorAll<HTMLElement>('[data-ui="status-readout"]')
    expect([...readouts].map((readout) => readout.textContent)).toEqual(['Readout a', 'Readout b', 'Readout c', 'Readout d'])
    expect(readouts[0].dataset.owner).toBe(PLUGIN)
    const more = screen.getByRole('note', { name: '2 more readouts: Readout e, Readout f' })
    expect(more.textContent).toBe('+2')
    expect(more.tabIndex).toBe(0)
  })

  it('shows a plugin page badge and makes it part of the entry name', async () => {
    const { extensions } = await mountShell()
    act(() => extensions.contribute(PLUGIN, { pages: [{ id: 'acme.tools/board', title: 'Board', badge: { text: '12', tone: 'danger' } }] }))
    const item = screen.getByRole('button', { name: 'Board, 12' })
    const badge = item.querySelector<HTMLElement>('[data-ui="nav-badge"]')
    expect(badge?.textContent).toBe('12')
    expect(badge?.dataset.tone).toBe('danger')
    expect(badge?.getAttribute('aria-hidden')).toBe('true')
  })

  it('runs a contributed command on its bound key, and only when it asked for a binding', async () => {
    const { extensions } = await mountShell()
    const bound = vi.fn()
    const hint = vi.fn()
    act(() => extensions.contribute(PLUGIN, {
      commands: [
        { id: `${PLUGIN}:lock`, title: 'Lock', group: 'Acme', shortcut: 'Alt+L', bind: true, run: bound },
        { id: `${PLUGIN}:hint`, title: 'Hint', group: 'Acme', shortcut: 'Alt+H', run: hint },
      ],
    }))
    fireEvent.keyDown(window, { code: 'KeyL', key: 'l', altKey: true })
    fireEvent.keyDown(window, { code: 'KeyH', key: 'h', altKey: true })
    await waitFor(() => expect(bound).toHaveBeenCalledOnce())
    expect(hint).not.toHaveBeenCalled()
    // The shell's own keys win: Ctrl+K still opens the palette, and a bound
    // key pressed inside it does nothing.
    fireEvent.keyDown(window, { code: 'KeyK', key: 'k', ctrlKey: true })
    const search = await screen.findByRole('combobox')
    fireEvent.keyDown(search, { code: 'KeyL', key: 'l', altKey: true })
    await Promise.resolve()
    expect(bound).toHaveBeenCalledOnce()
  })
})
