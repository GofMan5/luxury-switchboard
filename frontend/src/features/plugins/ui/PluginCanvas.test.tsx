// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { CanvasChannel, CanvasInput, DrawOp } from '../domain/canvas'
import type { PageEvent } from '../domain/protocol'
import { checkTree, type UiNode } from '../domain/ui-tree'
import { PluginTree } from './PluginTree'

const mocks = vi.hoisted(() => ({ openExternal: vi.fn(async () => undefined) }))
vi.mock('../../../platform/lifecycle/open-external', () => ({ openExternal: mocks.openExternal }))

afterEach(cleanup)
beforeEach(() => mocks.openExternal.mockClear())
vi.setConfig({ testTimeout: 20_000 })

function tree(raw: unknown): UiNode {
  const checked = checkTree(raw)
  if (!checked.ok) throw new Error(checked.problem)
  return checked.tree
}

function draw(raw: unknown) {
  const events: PageEvent[] = []
  const view = render(<PluginTree disabled={false} onEvent={(event) => events.push(event)} tree={tree(raw)} />)
  const redraw = (next: unknown) => view.rerender(<PluginTree disabled={false} onEvent={(event) => events.push(event)} tree={tree(next)} />)
  return { events, redraw, ...view }
}

describe('PluginTree richer nodes', () => {
  it('draws markdown as elements, never HTML, and opens its links outside', () => {
    const { container } = draw({ type: 'markdown', text: '## Notes\n\n**Bold** <img src=x onerror=alert(1)> [docs](https://example.test/d) [bad](javascript:alert(1))' })
    expect(container.querySelector('h3')?.textContent).toBe('Notes')
    expect(container.querySelector('strong')?.textContent).toBe('Bold')
    expect(container.querySelector('img')).toBeNull()
    expect(container.textContent).toContain('<img src=x onerror=alert(1)>')
    expect(container.querySelectorAll('a')).toHaveLength(1)
    fireEvent.click(screen.getByRole('link', { name: 'docs' }))
    expect(mocks.openExternal).toHaveBeenCalledWith('https://example.test/d')
  })

  it('draws a data image with its alt text', () => {
    draw({ type: 'image', src: 'data:image/png;base64,iVBORw0KGgo=', alt: 'Logo', width: 24, height: 24 })
    const image = screen.getByRole('img', { name: 'Logo' })
    expect(image.getAttribute('src')).toBe('data:image/png;base64,iVBORw0KGgo=')
    expect(image.getAttribute('width')).toBe('24')
  })

  it('sends a slider value as a number once the operator lets go, with the form', () => {
    const { events } = draw({ type: 'stack', children: [
      { type: 'slider', name: 'speed', label: 'Speed', min: 0, max: 10, step: 1, value: 3 },
      { type: 'button', text: 'Go', action: 'go' },
    ] })
    const slider = screen.getByRole('slider')
    fireEvent.change(slider, { target: { value: '7' } })
    expect(events).toEqual([])
    expect(screen.getByText('7')).toBeTruthy()
    fireEvent.keyUp(slider, { key: 'ArrowRight' })
    fireEvent.blur(slider)
    expect(events).toEqual([{ kind: 'change', name: 'speed', value: 7, form: { speed: 7 } }])
    fireEvent.click(screen.getByRole('button', { name: 'Go' }))
    expect(events[1]?.form).toEqual({ speed: 7 })
  })

  it('sends a colour when the picker commits it, lower-cased, once', () => {
    const { events } = draw({ type: 'color', name: 'tint', label: 'Tint', value: '#112233' })
    const input = screen.getByLabelText('Tint') as HTMLInputElement
    fireEvent.input(input, { target: { value: '#aabbcc' } })
    expect(events).toEqual([])
    input.dispatchEvent(new Event('change'))
    expect(events).toEqual([{ kind: 'change', name: 'tint', value: '#aabbcc', form: { tint: '#aabbcc' } }])
    fireEvent.blur(input)
    expect(events).toHaveLength(1)
  })

  it('opens details on toggle and follows the tree when it changes its mind', () => {
    const body = [{ type: 'text', text: 'Inside' }]
    const { redraw, container } = draw({ type: 'details', summary: 'Advanced', children: body })
    expect(screen.queryByText('Inside')).toBeNull()
    const details = container.querySelector('details')!
    details.open = true
    fireEvent(details, new Event('toggle'))
    expect(screen.getByText('Inside')).toBeTruthy()
    // The tree still says closed, so the operator's choice stands.
    redraw({ type: 'details', summary: 'Advanced', open: false, children: body })
    expect(screen.getByText('Inside')).toBeTruthy()
    redraw({ type: 'details', summary: 'Advanced', open: true, children: body })
    redraw({ type: 'details', summary: 'Advanced', open: false, children: body })
    expect(screen.queryByText('Inside')).toBeNull()
  })

  it('draws a labelled placeholder for a canvas without a channel', () => {
    draw({ type: 'canvas', id: 'board', width: 100, height: 50, label: 'Game board' })
    expect(screen.getByRole('img', { name: 'Game board' }).textContent).toBe('Canvas unavailable')
  })
})

describe('PluginCanvas', () => {
  function mount(node: Record<string, unknown>, disabled = false) {
    const inputs: CanvasInput[] = []
    const visible: [string, string, boolean, boolean][] = []
    let painter: ((ops: readonly DrawOp[]) => void) | undefined
    const unsubscribe = vi.fn()
    const channel: CanvasChannel = {
      subscribe: (_surface, _canvas, paint) => {
        painter = paint
        return unsubscribe
      },
      input: (_surface, _canvas, event) => inputs.push(event),
      visible: (surface, canvas, frames, shown) => visible.push([surface, canvas, frames, shown]),
    }
    const raw = { type: 'canvas', id: 'board', width: 200, height: 100, label: 'Board', ...node }
    const view = render(<PluginTree disabled={disabled} onEvent={() => undefined} tree={tree(raw)} canvas={channel} surface="page-1" />)
    return { inputs, visible, unsubscribe, paint: (ops: readonly DrawOp[]) => painter?.(ops), ...view, canvas: view.container.querySelector('canvas')! }
  }

  it('captures plain and Shift keys while focused, never Tab, chords or the safe-mode chord', () => {
    const { canvas, inputs } = mount({ keys: true })
    expect(screen.getByText('Click to focus')).toBeTruthy()
    fireEvent.focus(canvas)
    expect(screen.queryByText('Click to focus')).toBeNull()
    expect(fireEvent.keyDown(canvas, { key: 'ArrowLeft', code: 'ArrowLeft' })).toBe(false)
    fireEvent.keyDown(canvas, { key: 'A', code: 'KeyA', shiftKey: true })
    fireEvent.keyUp(canvas, { key: 'A', code: 'KeyA', shiftKey: true })
    const shellKeys = [
      { key: 'Tab', code: 'Tab' }, { key: 'Tab', code: 'Tab', shiftKey: true },
      { key: 'r', code: 'KeyR', ctrlKey: true }, { key: 'x', code: 'KeyX', altKey: true }, { key: 'k', code: 'KeyK', metaKey: true },
      { key: 'S', code: 'KeyS', ctrlKey: true, altKey: true, shiftKey: true },
    ]
    // fireEvent returns false when a handler called preventDefault.
    for (const key of shellKeys) expect(fireEvent.keyDown(canvas, key)).toBe(true)
    const seen = inputs.map((input) => input.kind === 'keydown' || input.kind === 'keyup' ? [input.kind, input.key, input.shift].join(':') : input.kind)
    expect(seen).toEqual(['focus', 'keydown:ArrowLeft:false', 'keydown:A:true', 'keyup:A:true'])
  })

  it('sends no keys when keys is off, and nothing while the plugin is stopped', () => {
    const off = mount({ keys: false, pointer: true })
    fireEvent.focus(off.canvas)
    expect(fireEvent.keyDown(off.canvas, { key: 'a', code: 'KeyA' })).toBe(true)
    expect(off.inputs.map((input) => input.kind)).toEqual(['focus'])
    cleanup()
    const stopped = mount({ keys: true, pointer: true }, true)
    fireEvent.focus(stopped.canvas)
    fireEvent.keyDown(stopped.canvas, { key: 'a', code: 'KeyA' })
    fireEvent.pointerDown(stopped.canvas, { clientX: 1, clientY: 1 })
    expect(stopped.inputs).toEqual([])
    expect(screen.queryByText('Click to focus')).toBeNull()
  })

  it('reports pointer input in canvas units and coalesces moves to 60 a second', () => {
    vi.useFakeTimers()
    try {
      const { canvas, inputs } = mount({ pointer: true })
      canvas.getBoundingClientRect = () => ({ left: 10, top: 20, width: 400, height: 200, right: 410, bottom: 220, x: 10, y: 20, toJSON: () => ({}) })
      fireEvent.pointerDown(canvas, { clientX: 210, clientY: 120, button: 0, buttons: 1 })
      expect(inputs[0]).toEqual({ surface: 'page-1', canvas: 'board', kind: 'pointerdown', x: 100, y: 50, button: 0, buttons: 1 })
      for (let step = 0; step < 10; step += 1) fireEvent.pointerMove(canvas, { clientX: 10 + step * 4, clientY: 20 })
      expect(inputs.filter((input) => input.kind === 'pointermove')).toHaveLength(1)
      vi.advanceTimersByTime(20)
      const moves = inputs.filter((input) => input.kind === 'pointermove')
      // Only the latest position of the burst is sent.
      expect(moves).toHaveLength(2)
      expect(moves[1]).toMatchObject({ x: 18, y: 0 })
      fireEvent.wheel(canvas, { clientX: 10, clientY: 20, deltaY: 120 })
      expect(inputs.at(-1)).toMatchObject({ kind: 'wheel', deltaY: 120 })
    } finally {
      vi.useRealTimers()
    }
  })

  it('subscribes, reports visibility and releases both on unmount', () => {
    const { visible, unsubscribe, unmount, paint } = mount({ frames: true })
    expect(visible).toEqual([['page-1', 'board', true, true]])
    paint([{ op: 'clear', color: 'token:background' }, { op: 'rect', x: 0, y: 0, w: 10, h: 10, fill: '#fff' }, { op: 'restore' }])
    unmount()
    expect(unsubscribe).toHaveBeenCalledOnce()
    expect(visible.at(-1)).toEqual(['page-1', 'board', true, false])
  })

  it('is labelled, and focusable only when it takes input', () => {
    const still = mount({})
    expect(still.canvas.getAttribute('aria-label')).toBe('Board')
    expect(still.canvas.getAttribute('role')).toBe('img')
    expect(still.canvas.hasAttribute('tabindex')).toBe(false)
    expect(screen.queryByText('Click to focus')).toBeNull()
    cleanup()
    const game = mount({ keys: true })
    expect(game.canvas.getAttribute('tabindex')).toBe('0')
  })
})
