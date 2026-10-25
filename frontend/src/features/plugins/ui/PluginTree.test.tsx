// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ServicesContext, type AppServices } from '../../../app/services'
import type { PageEvent } from '../domain/protocol'
import { checkTree, type UiNode } from '../domain/ui-tree'
import PluginPageView from './PluginPageView'
import { PluginTree } from './PluginTree'

const mocks = vi.hoisted(() => ({ openExternal: vi.fn(async () => undefined) }))
vi.mock('../../../platform/lifecycle/open-external', () => ({ openExternal: mocks.openExternal }))

afterEach(cleanup)
beforeEach(() => mocks.openExternal.mockClear())
// The first render pays for jsdom and the module graph; a loaded machine can pass the 5 s default.
vi.setConfig({ testTimeout: 20_000 })

function tree(raw: unknown): UiNode {
  const checked = checkTree(raw)
  if (!checked.ok) throw new Error(checked.problem)
  return checked.tree
}

function draw(raw: unknown, disabled = false) {
  const events: PageEvent[] = []
  const view = render(<PluginTree disabled={disabled} onEvent={(event) => events.push(event)} tree={tree(raw)} />)
  const redraw = (next: unknown, off = disabled) => view.rerender(<PluginTree disabled={off} onEvent={(event) => events.push(event)} tree={tree(next)} />)
  return { events, redraw, ...view }
}

function describedText(element: Element): string {
  return (element.getAttribute('aria-describedby') ?? '').split(' ').filter(Boolean)
    .map((id) => document.getElementById(id)?.textContent ?? '').join(' | ')
}

describe('PluginTree values', () => {
  it('formats numbers itself, keeping small fractions instead of rounding them to zero', () => {
    draw({ type: 'stack', children: [
      { type: 'metric', label: 'Error rate', value: 0.0004 },
      { type: 'metrics', items: [{ label: 'Requests', value: 1234567 }, { label: 'Healthy', value: true }, { label: 'Unknown', value: null }] },
      { type: 'kv', items: [{ key: 'Ratio', value: 12.5 }, { key: 'Cached', value: false }] },
      { type: 'table', columns: [{ key: 'share', label: 'Share', format: 'percent' }, { key: 'size', label: 'Size', format: 'bytes' }, { key: 'took', label: 'Took', format: 'duration' }], rows: [{ share: 0.5, size: 1536, took: 125_000 }] },
    ] })
    expect(screen.getByText('0.0004')).toBeTruthy()
    expect(screen.queryByText('0')).toBeNull()
    expect(screen.getByText('1,234,567')).toBeTruthy()
    expect(screen.getByText('Yes')).toBeTruthy()
    expect(screen.getByText('—')).toBeTruthy()
    expect(screen.getByText('12.5')).toBeTruthy()
    expect(screen.getByText('No')).toBeTruthy()
    const share = screen.getByText('50%')
    expect(share.getAttribute('data-align')).toBe('right')
    expect(screen.getByText('1.5 KiB')).toBeTruthy()
    expect(screen.getByText('2m 05s')).toBeTruthy()
  })

  it('titles long table cells with their full text', () => {
    const long = 'x'.repeat(41)
    draw({ type: 'table', columns: [{ key: 'a', label: 'A' }], rows: [{ a: long }, { a: 'short' }] })
    expect(screen.getByText(long).getAttribute('title')).toBe(long)
    expect(screen.getByText('short').hasAttribute('title')).toBe(false)
  })
})

describe('PluginTree fields', () => {
  it('ties hint, required and error to the input, and checks a number field’s range', () => {
    const { redraw } = draw({ type: 'input', name: 'port', label: 'Port', kind: 'number', min: 1, max: 10, hint: 'From 1 to 10', required: true })
    const box = screen.getByRole('textbox', { name: 'Port' }) as HTMLInputElement
    expect(box.getAttribute('aria-required')).toBe('true')
    expect(box.hasAttribute('aria-invalid')).toBe(false)
    expect(describedText(box)).toBe('From 1 to 10')
    expect(box.getAttribute('inputmode')).toBe('decimal')

    fireEvent.change(box, { target: { value: '42' } })
    expect(box.getAttribute('aria-invalid')).toBe('true')
    expect(describedText(box)).toBe('From 1 to 10 | Enter a number from 1 to 10.')

    // Arrow keys step, kept inside the range.
    fireEvent.keyDown(box, { key: 'ArrowUp' })
    expect(box.value).toBe('10')
    fireEvent.keyDown(box, { key: 'ArrowDown' })
    expect(box.value).toBe('9')
    expect(box.hasAttribute('aria-invalid')).toBe(false)

    // The plugin's own message wins.
    redraw({ type: 'input', name: 'port', label: 'Port', kind: 'number', value: 3, error: 'Port 3 is taken' })
    const again = screen.getByRole('textbox', { name: 'Port' })
    expect(again.getAttribute('aria-invalid')).toBe('true')
    expect(describedText(again)).toBe('Port 3 is taken')
    expect((again as HTMLInputElement).value).toBe('3')
  })

  it('sends a textarea with Ctrl+Enter and commits it on blur', () => {
    const { events } = draw({ type: 'textarea', name: 'note', label: 'Note', value: 'a', action: 'save', hint: 'Ctrl+Enter saves' })
    const box = screen.getByRole('textbox', { name: 'Note' }) as HTMLTextAreaElement
    expect(box.rows).toBe(4)
    expect(box.maxLength).toBe(1000)
    fireEvent.change(box, { target: { value: 'line one\nline two' } })
    fireEvent.keyDown(box, { key: 'Enter' })
    expect(events).toEqual([])
    fireEvent.keyDown(box, { key: 'Enter', ctrlKey: true })
    expect(events).toEqual([{ kind: 'action', action: 'save', name: 'note', value: 'line one\nline two', form: { note: 'line one\nline two' } }])
    fireEvent.blur(box)
    expect(events).toHaveLength(1)
    fireEvent.change(box, { target: { value: 'edited' } })
    fireEvent.blur(box)
    expect(events[1]).toEqual({ kind: 'change', name: 'note', value: 'edited', form: { note: 'edited' } })
  })

  it('moves and chooses segmented options with arrows, Home and End, keeping one tab stop', () => {
    const { events } = draw({ type: 'segmented', name: 'period', label: 'Period', options: ['1h', '24h', '7d'], value: '1h' })
    expect(screen.getByRole('radiogroup', { name: 'Period' })).toBeTruthy()
    const radios = () => screen.getAllByRole('radio')
    expect(radios().map((radio) => radio.tabIndex)).toEqual([0, -1, -1])
    radios()[0].focus()
    fireEvent.keyDown(radios()[0], { key: 'ArrowRight' })
    expect(document.activeElement).toBe(radios()[1])
    expect(radios()[1].getAttribute('aria-checked')).toBe('true')
    expect(radios().map((radio) => radio.tabIndex)).toEqual([-1, 0, -1])
    fireEvent.keyDown(radios()[1], { key: 'ArrowDown' })
    fireEvent.keyDown(radios()[2], { key: 'ArrowRight' })
    fireEvent.keyDown(radios()[0], { key: 'End' })
    fireEvent.keyDown(radios()[2], { key: 'Home' })
    expect(events.map((event) => event.value)).toEqual(['24h', '7d', '1h', '7d', '1h'])
    expect(events[0]).toEqual({ kind: 'change', name: 'period', value: '24h', form: { period: '24h' } })
    expect(document.activeElement).toBe(radios()[0])
  })

  it('keeps a field’s draft while siblings come and go, and takes the plugin’s value when it changes', () => {
    const field = { type: 'input', name: 'q', label: 'Search', value: 'a' }
    const { events, redraw } = draw({ type: 'stack', children: [field, { type: 'button', text: 'Go', action: 'go' }] })
    const box = screen.getByRole('textbox', { name: 'Search' }) as HTMLInputElement
    fireEvent.change(box, { target: { value: 'draft' } })

    // A new node before it shifts its index, not its identity.
    redraw({ type: 'stack', children: [{ type: 'text', text: 'Now with a note' }, field, { type: 'button', text: 'Go', action: 'go' }] })
    expect(screen.getByRole('textbox', { name: 'Search' })).toBe(box)
    expect(box.value).toBe('draft')

    redraw({ type: 'stack', children: [{ ...field, value: 'from plugin' }, { type: 'button', text: 'Go', action: 'go' }] })
    expect(box.value).toBe('from plugin')
    fireEvent.click(screen.getByRole('button', { name: 'Go' }))
    expect(events.at(-1)).toEqual({ kind: 'action', action: 'go', form: { q: 'from plugin' } })

    // A field that leaves the page leaves the form.
    redraw({ type: 'stack', children: [{ type: 'button', text: 'Go', action: 'go' }] })
    fireEvent.click(screen.getByRole('button', { name: 'Go' }))
    expect(events.at(-1)).toEqual({ kind: 'action', action: 'go', form: {} })
  })
})

describe('PluginTree tabs', () => {
  const raw = { type: 'stack', children: [
    { type: 'tabs', name: 'view', label: 'View', tabs: [
      { id: 'a', label: 'Alpha', children: [{ type: 'text', text: 'Alpha body' }] },
      { id: 'b', label: 'Beta', children: [{ type: 'text', text: 'Beta body' }] },
      { id: 'c', label: 'Gamma', children: [{ type: 'text', text: 'Gamma body' }] },
    ] },
    { type: 'button', text: 'Go', action: 'go' },
  ] }

  it('renders only the open tab and moves with arrows, Home and End', () => {
    const { events } = draw(raw)
    expect(screen.getByRole('tablist', { name: 'View' })).toBeTruthy()
    const tabs = () => screen.getAllByRole('tab')
    const panel = () => screen.getByRole('tabpanel')
    expect(tabs().map((tab) => tab.getAttribute('aria-selected'))).toEqual(['true', 'false', 'false'])
    expect(tabs().map((tab) => tab.tabIndex)).toEqual([0, -1, -1])
    expect(tabs()[0].getAttribute('aria-controls')).toBe(panel().id)
    expect(panel().getAttribute('aria-labelledby')).toBe(tabs()[0].id)
    expect(panel().textContent).toBe('Alpha body')
    expect(screen.queryByText('Beta body')).toBeNull()

    tabs()[0].focus()
    fireEvent.keyDown(tabs()[0], { key: 'ArrowRight' })
    expect(document.activeElement).toBe(tabs()[1])
    expect(tabs()[1].getAttribute('aria-selected')).toBe('true')
    expect(panel().textContent).toBe('Beta body')
    expect(panel().getAttribute('aria-labelledby')).toBe(tabs()[1].id)
    expect(events[0]).toEqual({ kind: 'change', name: 'view', value: 'b', form: { view: 'b' } })

    fireEvent.keyDown(tabs()[1], { key: 'End' })
    expect(panel().textContent).toBe('Gamma body')
    fireEvent.keyDown(tabs()[2], { key: 'ArrowRight' })
    expect(panel().textContent).toBe('Alpha body')
    fireEvent.keyDown(tabs()[0], { key: 'ArrowLeft' })
    expect(document.activeElement).toBe(tabs()[2])
    fireEvent.keyDown(tabs()[2], { key: 'Home' })
    expect(events.map((event) => event.value)).toEqual(['b', 'c', 'a', 'c', 'a'])

    fireEvent.click(tabs()[1])
    fireEvent.click(screen.getByRole('button', { name: 'Go' }))
    expect(events.at(-1)).toEqual({ kind: 'action', action: 'go', form: { view: 'b' } })
  })

  it('still switches while the plugin is not running, without sending', () => {
    const { events } = draw(raw, true)
    fireEvent.click(screen.getByRole('tab', { name: 'Beta' }))
    expect(screen.getByRole('tabpanel').textContent).toBe('Beta body')
    expect(events).toEqual([])
  })
})

describe('PluginTree display nodes', () => {
  it('opens links in the system browser from a real anchor', () => {
    draw({ type: 'link', text: 'Docs', href: 'https://example.test/docs' })
    const link = screen.getByRole('link', { name: 'Docs' })
    expect(link.getAttribute('href')).toBe('https://example.test/docs')
    expect(link.querySelector('svg')).toBeTruthy()
    // dispatchEvent answers false when the default was prevented: the webview does not navigate.
    expect(fireEvent.click(link)).toBe(false)
    expect(mocks.openExternal).toHaveBeenCalledWith('https://example.test/docs')
  })

  it('draws a chart as one named image with a text summary', () => {
    const { redraw } = draw({ type: 'chart', label: 'Latency', values: [10, 30.5, 20], labels: ['Mon', 'Tue', 'Wed'], tone: 'warning' })
    const chart = screen.getByRole('img', { name: 'Latency' })
    expect(describedText(chart)).toBe('3 values; lowest 10, highest 30.5, last Wed: 20.')
    expect(chart.querySelector('polyline')?.getAttribute('points')?.split(' ')).toHaveLength(3)
    expect(chart.closest('figure')?.getAttribute('data-tone')).toBe('warning')

    redraw({ type: 'chart', kind: 'bars', label: 'Hits', values: [1, -2], height: 48 })
    const bars = screen.getByRole('img', { name: 'Hits' })
    expect(bars.querySelectorAll('rect')).toHaveLength(2)
    expect((bars as unknown as SVGElement).style.height).toBe('48px')
  })

  it('draws callouts as notes with their own actions', () => {
    const { events } = draw({ type: 'callout', tone: 'danger', title: 'Key expired', text: 'Paste a new one.', actions: [{ type: 'button', text: 'Fix', action: 'fix' }] })
    const note = screen.getByRole('note')
    expect(note.getAttribute('data-tone')).toBe('danger')
    expect(within(note).getByText('Key expired')).toBeTruthy()
    expect(within(note).getByText('Paste a new one.')).toBeTruthy()
    fireEvent.click(within(note).getByRole('button', { name: 'Fix' }))
    expect(events).toEqual([{ kind: 'action', action: 'fix', form: {} }])
  })

  it('leaves the value out of an indeterminate progress bar', () => {
    draw({ type: 'stack', children: [{ type: 'progress', label: 'Sync', indeterminate: true }, { type: 'progress', label: 'Load', value: 0.4 }] })
    const sync = screen.getByRole('progressbar', { name: 'Sync' })
    expect(sync.hasAttribute('aria-valuenow')).toBe(false)
    expect(sync.getAttribute('aria-busy')).toBe('true')
    expect(screen.getByRole('progressbar', { name: 'Load' }).getAttribute('aria-valuenow')).toBe('40')
  })
})

describe('PluginPageView', () => {
  function services(raw: unknown) {
    const state = {
      phase: 'ready', pending: '', error: '', safeMode: false, asking: null,
      plugins: [{ id: 'acme.tools', name: 'Acme tools', version: '1.0.0', enabled: true }],
      runtimes: { 'acme.tools': { state: 'running', reason: '', faults: 0, logs: [], pages: [], commands: 0, status: 0 } },
      pages: { 'acme.tools/board': { key: 'acme.tools/board', pluginId: 'acme.tools', pluginName: 'Acme tools', pageId: 'board', title: 'Board', tree: tree(raw), problem: '' } },
    }
    const sendPageEvent = vi.fn()
    const plugins = { subscribe: () => () => undefined, snapshot: () => state, sendPageEvent, restart: vi.fn(), canvas: () => undefined }
    return { services: { plugins } as unknown as AppServices, sendPageEvent }
  }

  it('sends show and hide with the page’s real form', () => {
    const { services: value, sendPageEvent } = services({ type: 'stack', children: [
      { type: 'input', name: 'q', label: 'Search', value: 'north' },
      { type: 'toggle', name: 'live', label: 'Live', value: true },
      { type: 'tabs', name: 'view', tabs: [{ id: 'list', label: 'List' }, { id: 'map', label: 'Map' }] },
    ] })
    const view = render(<ServicesContext value={value}><PluginPageView pageId="acme.tools/board" /></ServicesContext>)
    expect(sendPageEvent).toHaveBeenCalledWith('acme.tools/board', { kind: 'show', form: { q: 'north', live: true, view: 'list' } })

    fireEvent.change(screen.getByRole('textbox', { name: 'Search' }), { target: { value: 'south' } })
    view.unmount()
    expect(sendPageEvent).toHaveBeenLastCalledWith('acme.tools/board', { kind: 'hide', form: { q: 'south', live: true, view: 'list' } })
    expect(sendPageEvent).toHaveBeenCalledTimes(2)
  })
})
