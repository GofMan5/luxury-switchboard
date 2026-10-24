import { describe, expect, it } from 'vitest'
import { NODE_TYPES, TREE_LIMITS, checkTree, type UiNode } from './ui-tree'

function ok(raw: unknown): UiNode {
  const checked = checkTree(raw)
  if (!checked.ok) throw new Error(checked.problem)
  return checked.tree
}

function problem(raw: unknown): string {
  const checked = checkTree(raw)
  return checked.ok ? '' : checked.problem
}

describe('checkTree', () => {
  it('fills defaults and returns a clean copy without unknown fields', () => {
    const raw = { type: 'stack', onclick: 'alert(1)', children: [{ type: 'text', text: 'Hi', style: 'color:red' }, null, false, { type: 'badge', text: 'ok' }] }
    expect(ok(raw)).toEqual({
      type: 'stack', gap: 'md', children: [
        { type: 'text', text: 'Hi', tone: 'default', mono: false, size: 'md', weight: 'regular' },
        { type: 'badge', text: 'ok', tone: 'neutral' },
      ],
    })
  })

  it('accepts one of every node type', () => {
    const tree = {
      type: 'stack', children: [
        { type: 'row', align: 'between', children: [{ type: 'heading', text: 'Board', level: 1 }, { type: 'button', text: 'Go', action: 'go', variant: 'primary', icon: 'zap' }] },
        { type: 'grid', columns: 3, children: [{ type: 'metric', label: 'Requests', value: 12 }, { type: 'dot', tone: 'success', text: 'Live' }, { type: 'divider' }] },
        { type: 'panel', title: 'Panel', actions: [{ type: 'link', text: 'Docs', href: 'https://example.test/docs' }], children: [{ type: 'empty', title: 'Nothing' }] },
        { type: 'metrics', items: [{ label: 'p95', value: '1.2 s', tone: 'warning' }] },
        { type: 'table', columns: [{ key: 'name' }, { key: 'ms', align: 'right', mono: true, width: 80 }], rows: [{ id: 'a', name: 'North', ms: 120 }], rowAction: 'open' },
        { type: 'list', items: [{ title: 'One', detail: 'first', meta: '1', tone: 'info' }] },
        { type: 'code', text: 'const a = 1' },
        { type: 'kv', items: [{ key: 'Region', value: 'eu' }, { key: 'Up', value: true }] },
        { type: 'input', name: 'q', label: 'Search', kind: 'search', action: 'search' },
        { type: 'select', name: 'period', label: 'Period', options: ['1h', { value: '24h', label: 'Day' }], value: '24h' },
        { type: 'toggle', name: 'live', label: 'Live', value: true },
        { type: 'segmented', name: 'view', label: 'View', options: ['a', 'b'] },
        { type: 'progress', value: 0.4, label: 'Load' },
        { type: 'text', text: 'Muted', tone: 'muted', mono: true },
        { type: 'badge', text: 'beta', tone: 'warning' },
      ],
    }
    const seen = new Set<string>()
    const walk = (node: UiNode) => {
      seen.add(node.type)
      if ('children' in node) node.children.forEach(walk)
      if (node.type === 'panel') node.actions.forEach(walk)
    }
    walk(ok(tree))
    expect([...seen].sort()).toEqual([...NODE_TYPES].sort())
  })

  it('names the exact path of the first problem', () => {
    expect(problem({ type: 'stack', children: [{ type: 'text', text: 'a' }, { type: 'table', columns: [{ key: 'a' }], rows: [{ a: 1 }, { a: {} }] }] }))
      .toBe('tree.children[1].rows[1].a must be a string.')
    expect(problem({ type: 'stack', children: [{ type: 'blink' }] })).toMatch(/^tree\.children\[0\]\.type "blink" is not a node type/u)
    expect(problem({ type: 'button', text: 'x', action: 'has space' })).toBe('tree.action must be 1–64 letters, digits, dots, colons, dashes or underscores.')
    expect(problem({ type: 'grid', columns: 7 })).toBe('tree.columns must be a whole number from 1 to 6.')
    expect(problem({ type: 'text', text: 'x', tone: 'loud' })).toMatch(/^tree\.tone must be one of default, muted/u)
  })

  it('refuses links that are not https or carry credentials', () => {
    for (const href of ['http://example.test', 'javascript:alert(1)', 'file:///etc/passwd', 'https://user:secret@example.test/', 'not a url']) {
      expect(problem({ type: 'link', text: 'x', href })).toBe('tree.href must be an https:// address without credentials.')
    }
  })

  it('clamps progress and refuses non-numbers', () => {
    expect(ok({ type: 'progress', value: 3 })).toMatchObject({ value: 1 })
    expect(ok({ type: 'progress', value: -1 })).toMatchObject({ value: 0, tone: 'info' })
    expect(problem({ type: 'progress', value: '50%' })).toBe('tree.value must be a number from 0 to 1.')
    expect(problem({ type: 'progress', value: Number.NaN })).toBe('tree.value must be a number from 0 to 1.')
  })

  it('needs options and columns, and caps segmented controls at six', () => {
    expect(problem({ type: 'select', name: 'a', label: 'A', options: [] })).toBe('tree.options needs at least one option.')
    expect(problem({ type: 'table', columns: [], rows: [] })).toBe('tree.columns needs at least one column.')
    expect(problem({ type: 'segmented', name: 'a', label: 'A', options: ['1', '2', '3', '4', '5', '6', '7'] })).toBe('tree.options has 7 entries; at most 6.')
  })

  it('reads table widths and keeps only declared cells', () => {
    const table = ok({ type: 'table', columns: [{ key: 'a', width: '20%' }, { key: 'b' }], rows: [{ a: 1, b: null, c: 'dropped', id: 7 }] })
    expect(table).toMatchObject({ columns: [{ width: '20%' }, { width: 'auto' }], rows: [{ a: 1, b: null, id: 7 }], empty: 'Nothing to show yet.' })
    expect(problem({ type: 'table', columns: [{ key: 'a', width: 'calc(1px)' }], rows: [] })).toMatch(/width must be a pixel number/u)
  })

  it('formats numbers and booleans in metrics and kv lists', () => {
    expect(ok({ type: 'metric', label: 'Up', value: true })).toMatchObject({ value: 'Yes' })
    expect(ok({ type: 'kv', items: [{ key: 'n', value: Number.POSITIVE_INFINITY }] })).toMatchObject({ items: [{ key: 'n', value: '—' }] })
  })

  it('caps depth', () => {
    let tree: Record<string, unknown> = { type: 'text', text: 'leaf' }
    for (let level = 0; level < TREE_LIMITS.depth; level += 1) tree = { type: 'stack', children: [tree] }
    expect(checkTree(tree).ok).toBe(true)
    expect(problem({ type: 'stack', children: [tree] })).toMatch(/nests deeper than 24 levels\.$/u)
  })

  it('caps nodes, counting rows and items', () => {
    const rows = Array.from({ length: TREE_LIMITS.rows }, (_, index) => ({ a: index }))
    const tables = Array.from({ length: 4 }, () => ({ type: 'table', columns: [{ key: 'a' }], rows }))
    expect(problem({ type: 'stack', children: tables })).toMatch(/takes the page over 2000 nodes\.$/u)
  })

  it('caps bytes and refuses cycles', () => {
    expect(problem({ type: 'code', text: 'x'.repeat(TREE_LIMITS.bytes) })).toMatch(/^The tree is \d+ KiB; a page takes at most 256 KiB\.$/u)
    const cyclic: Record<string, unknown> = { type: 'stack' }
    cyclic.children = [cyclic]
    expect(problem(cyclic)).toBe('The tree is not plain data (it has a cycle or a value JSON cannot hold).')
  })

  it('refuses non-object roots', () => {
    expect(problem(null)).toBe('tree must be an object.')
    expect(problem([{ type: 'text', text: 'a' }])).toBe('tree must be an object.')
  })
})
