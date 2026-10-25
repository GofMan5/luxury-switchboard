// @vitest-environment jsdom

import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ServicesContext, type AppServices } from '../../../app/services'
import { FixtureRules } from '../../../dev/fixture-rules'
import type { ControlPlaneSession } from '../../../platform/stdio/session'
import { ControlPlaneError } from '../../../shared/contracts/protocol'
import { StdioRulesPort } from '../adapters/stdio-rules-port'
import { RulesModel } from '../application/rules-model'
import RulesPage from './RulesPage'

/** The page against the development fixture, through the real adapter and
 * model, so a rules.changed event takes the same path as in the app. */
function mount(options: { failList?: boolean; failErrors?: boolean } = {}) {
  const listeners = new Set<() => void>()
  const fixture = new FixtureRules(() => { for (const listener of listeners) listener() })
  const calls: string[] = []
  const session: ControlPlaneSession = {
    async start() {},
    async stop() {},
    async call<T>(method: string, payload?: unknown): Promise<T> {
      calls.push(method)
      if (options.failList && method === 'rules.list') throw new ControlPlaneError('secure_storage_unavailable', 'Encrypted storage is unavailable')
      if (options.failErrors && method === 'rules.recentErrors') throw new ControlPlaneError('history_unavailable', 'Request history is unavailable')
      return fixture.answer(method, (payload ?? {}) as Record<string, unknown>) as T
    },
    subscribe(_topic, listener) {
      const wrapped = () => listener({ v: 1, type: 'event', topic: 'rules.changed', seq: 1, payload: {} } as never)
      listeners.add(wrapped)
      return () => listeners.delete(wrapped)
    },
  }
  const model = new RulesModel(new StdioRulesPort(session))
  const providerState = { catalog: { providers: [{ id: 'north-relay', name: 'North Relay' }, { id: 'sigma-llm', name: 'Sigma LLM' }] } }
  const services = {
    rules: model,
    providers: { subscribe: () => () => undefined, snapshot: () => providerState },
  } as unknown as AppServices
  // The services connect the model when the handshake advertises rules.manage.
  void model.connect()
  render(<ServicesContext.Provider value={services}><RulesPage /></ServicesContext.Provider>)
  return { model, calls, fixture }
}

const requestList = () => screen.findByRole('list', { name: 'Request rules, in evaluation order' })
const retryList = () => screen.findByRole('list', { name: 'Retry rules, in evaluation order' })
const rowNames = (list: HTMLElement) => within(list).getAllByRole('listitem').map((item) => item.querySelector('strong')?.textContent)

beforeEach(() => window.history.replaceState(null, '', '#rules'))
afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

describe('RulesPage', () => {
  it('lists request rules in order with their effects, counters and plugin owner', async () => {
    mount()
    const list = await requestList()
    expect(rowNames(list)).toEqual(['Cheap drafts', 'Image budget', 'Team header', 'No embeddings'])
    expect(within(list).getByText('Plugin · team-tags')).toBeTruthy()
    expect(within(list).getByText('Send to Sigma LLM')).toBeTruthy()
    expect(screen.getByRole('switch', { name: 'No embeddings enabled' }).getAttribute('aria-checked')).toBe('false')
    expect(screen.getByRole('button', { name: 'Move Cheap drafts up' })).toHaveProperty('disabled', true)
  })

  it('toggles and reorders through the control plane and follows rules.changed', async () => {
    const { calls } = mount()
    await requestList()
    fireEvent.click(screen.getByRole('switch', { name: 'Team header enabled' }))
    await waitFor(() => expect(screen.getByRole('switch', { name: 'Team header enabled' }).getAttribute('aria-checked')).toBe('false'))
    // The plugin's rule stays the plugin's after the operator switches it off.
    expect(screen.getByText('Plugin · team-tags')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Move Cheap drafts down' }))
    await waitFor(async () => expect(rowNames(await requestList())).toEqual(['Image budget', 'Cheap drafts', 'Team header', 'No embeddings']))
    expect(calls).toContain('rules.reorderRequestRules')
  })

  it('deletes a rule after confirmation', async () => {
    mount()
    await requestList()
    fireEvent.click(screen.getByRole('button', { name: 'Delete Image budget' }))
    const dialog = screen.getByRole('alertdialog', { name: 'Delete “Image budget”?' })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete rule' }))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    expect(rowNames(await requestList())).not.toContain('Image budget')
  })

  it('shows why a new rule is refused, then saves it', async () => {
    mount()
    await requestList()
    fireEvent.click(screen.getByRole('button', { name: 'Retry rules' }))
    expect(window.location.hash).toBe('#rules?tab=retry')
    await retryList()
    fireEvent.click(screen.getByRole('button', { name: /New retry rule/u }))
    const editor = screen.getByRole('dialog', { name: 'New retry rule' })
    fireEvent.click(within(editor).getByRole('button', { name: 'Save rule' }))
    expect(within(editor).getByLabelText('Name').getAttribute('aria-invalid')).toBe('true')

    fireEvent.change(within(editor).getByLabelText('Name'), { target: { value: 'Gateway timeout' } })
    fireEvent.change(within(editor).getByLabelText('HTTP statuses'), { target: { value: '504' } })
    expect((within(editor).getByLabelText('Id') as HTMLInputElement).value).toBe('gateway-timeout')
    fireEvent.click(within(editor).getByRole('button', { name: 'Save rule' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(rowNames(await retryList())).toContain('Gateway timeout')
  })

  it('tests a request against the rules without sending it upstream', async () => {
    mount()
    await requestList()
    const tester = screen.getByRole('region', { name: 'Test a request' })
    fireEvent.change(within(tester).getByLabelText('Model'), { target: { value: 'gpt-4o-mini' } })
    fireEvent.click(within(tester).getByRole('button', { name: 'Test' }))
    const result = await within(tester).findByRole('status')
    await waitFor(() => expect(result.textContent).toContain('Cheap drafts'))
    expect(result.textContent).toContain('Team header')
  })

  it('offers a retry rule for an uncovered error and names the rule covering another', async () => {
    mount()
    await requestList()
    fireEvent.click(screen.getByRole('button', { name: 'Recent errors' }))
    const table = await screen.findByRole('table')
    expect(within(table).getByRole('button', { name: 'Covered by Overloaded: switch provider. Edit the rule' })).toBeTruthy()
    const uncovered = within(table).getByText(/context_length_exceeded/u).closest('tr') as HTMLElement
    fireEvent.click(within(uncovered).getByRole('button', { name: 'Create retry rule' }))
    const editor = screen.getByRole('dialog', { name: 'New retry rule' })
    expect((within(editor).getByLabelText('HTTP statuses') as HTMLInputElement).value).toBe('400')
    expect((within(editor).getByLabelText('Name') as HTMLInputElement).value).toBe('Handle 400 upstream_status')
  })

  it('opens a prefilled editor once from a link and clears the draft from the address', async () => {
    window.history.replaceState(null, '', '#rules?tab=retry&draft=1&status=502&code=stream_incomplete')
    mount()
    const editor = await screen.findByRole('dialog', { name: 'New retry rule' })
    expect((within(editor).getByLabelText('Error codes') as HTMLInputElement).value).toBe('stream_incomplete')
    expect(window.location.hash).toBe('#rules?tab=retry')
  })

  it('says when rules or history are unavailable instead of showing them empty', async () => {
    mount({ failList: true })
    expect((await screen.findByRole('alert')).textContent).toContain('Encrypted storage is unavailable')
    expect(screen.queryByText('No request rules')).toBeNull()
    cleanup()

    mount({ failErrors: true })
    await requestList()
    fireEvent.click(screen.getByRole('button', { name: 'Recent errors' }))
    expect((await screen.findByRole('alert')).textContent).toContain('Request history is unavailable')
    expect(screen.queryByText('No recent errors')).toBeNull()
  })

  it('shows a change made elsewhere', async () => {
    const { fixture } = mount()
    await requestList()
    await act(async () => { fixture.answer('rules.deleteRequestRule', { id: 'cheap-drafts' }) })
    await waitFor(async () => expect(rowNames(await requestList())).not.toContain('Cheap drafts'))
  })
})
