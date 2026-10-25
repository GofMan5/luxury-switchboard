// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ExtensionPoints, type ActionContribution, type SlotContribution } from '../shared/contracts/extensions'
import { ActionButtons, InspectorTabs, SlotGrid } from './Contributed'
import { useContributed } from './contributions'
import { ServicesContext, type AppServices } from './services'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

const OWNER = 'plugin:acme'

function slot(id: string, scope: SlotContribution['scope'], render: SlotContribution['render'], extra: Partial<SlotContribution> = {}): SlotContribution {
  return { id: `${OWNER}:${id}`, owner: 'Acme', scope, title: id, render, ...extra }
}

function mount(ui: React.ReactNode) {
  const extensions = new ExtensionPoints()
  const view = render(<ServicesContext.Provider value={{ extensions } as unknown as AppServices}>{ui}</ServicesContext.Provider>)
  return { extensions, ...view }
}

function Thrower(): never {
  throw new Error('plugin text that must not be shown')
}

describe('SlotGrid', () => {
  it('renders nothing at all until a slot arrives, then frames it with its owner', () => {
    const { extensions, container } = mount(<SlotGrid scope="overview" />)
    expect(container.innerHTML).toBe('')
    act(() => extensions.contribute(OWNER, { slots: [slot('Latency', 'overview', () => <p>42 ms</p>, { description: 'Last hour', wide: true })] }))
    const frame = screen.getByRole('region', { name: 'Latency, from Acme' })
    expect(frame.textContent).toContain('From Acme')
    expect(frame.textContent).toContain('Last hour')
    expect(frame.textContent).toContain('42 ms')
    expect(frame.dataset.wide).toBe('true')
    act(() => extensions.clear(OWNER))
    expect(container.innerHTML).toBe('')
  })

  it('keeps a failing slot inside its frame with neutral copy, and retries on request', () => {
    vi.spyOn(console, 'error').mockImplementation(() => undefined)
    let fail = true
    const { extensions } = mount(<SlotGrid scope="overview" />)
    act(() => extensions.contribute(OWNER, { slots: [
      slot('Broken', 'overview', () => (fail ? <Thrower /> : <p>Recovered</p>)),
      slot('Fine', 'overview', () => <p>Still here</p>),
    ] }))
    const broken = screen.getByRole('region', { name: 'Broken, from Acme' })
    expect(broken.textContent).toContain('This section could not be displayed.')
    expect(broken.textContent).not.toContain('must not be shown')
    expect(screen.getByText('Still here')).toBeTruthy()
    fail = false
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
    expect(screen.getByText('Recovered')).toBeTruthy()
  })
})

describe('InspectorTabs', () => {
  it('adds no tab strip without slots, and a keyboard tablist with them', () => {
    const { extensions } = mount(<InspectorTabs scope="activity" id="req-1" label="Request sections"><p>Own details</p></InspectorTabs>)
    expect(screen.queryByRole('tablist')).toBeNull()
    expect(screen.getByText('Own details')).toBeTruthy()
    const seen: unknown[] = []
    act(() => extensions.contribute(OWNER, { slots: [
      slot('Trace', 'activity', (context) => { seen.push(context); return <p>Trace body</p> }),
      slot('Cost', 'activity', () => <p>Cost body</p>),
      slot('Elsewhere', 'provider', () => <p>Wrong scope</p>),
    ] }))
    const tabs = screen.getAllByRole('tab')
    expect(tabs.map((tab) => tab.textContent)).toEqual(['Details', 'Cost', 'Trace'])
    expect(tabs[0].getAttribute('aria-selected')).toBe('true')
    fireEvent.keyDown(tabs[0], { key: 'End' })
    expect(screen.getByRole('tab', { name: 'Trace' }).getAttribute('aria-selected')).toBe('true')
    expect(document.activeElement?.textContent).toBe('Trace')
    expect(screen.getByRole('tabpanel').textContent).toContain('From Acme')
    expect(screen.getByText('Trace body')).toBeTruthy()
    expect(seen.at(-1)).toEqual({ scope: 'activity', id: 'req-1' })
    fireEvent.keyDown(document.activeElement!, { key: 'ArrowRight' })
    expect(screen.getByText('Own details')).toBeTruthy()
    fireEvent.keyDown(document.activeElement!, { key: 'ArrowLeft' })
    expect(screen.getByText('Trace body')).toBeTruthy()
    // The chosen slot going away falls back to the inspector's own details.
    act(() => extensions.contribute(OWNER, { slots: [slot('Cost', 'activity', () => <p>Cost body</p>)] }))
    expect(screen.getByText('Own details')).toBeTruthy()
  })
})

describe('ActionButtons', () => {
  function Actions({ compact }: { readonly compact?: boolean }) {
    const { actions } = useContributed(undefined, 'key')
    return <ActionButtons actions={actions} targetId="key-7" compact={compact} />
  }

  it('runs each action with the record id and names its owner', () => {
    const run = vi.fn()
    const action: ActionContribution = { id: `${OWNER}:rotate`, owner: 'Acme', scope: 'key', title: 'Rotate', icon: 'refresh-cw', run }
    const { extensions, container } = mount(<Actions compact />)
    expect(container.innerHTML).toBe('')
    act(() => extensions.contribute(OWNER, { actions: [action, { ...action, id: `${OWNER}:other`, scope: 'activity' }] }))
    const button = screen.getByRole('button', { name: 'Rotate (from Acme)' })
    expect(button.textContent).toBe('')
    fireEvent.click(button)
    expect(run).toHaveBeenCalledWith('key-7')
    expect(screen.getAllByRole('button')).toHaveLength(1)
  })
})
