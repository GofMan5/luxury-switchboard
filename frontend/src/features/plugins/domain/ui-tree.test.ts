import { describe, expect, it } from 'vitest'
import { NODE_TYPES, TREE_LIMITS, checkTree, formatNumber, formatScalar, type UiNode } from './ui-tree'

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
  it('fills defaults and returns a clean copy', () => {
    const raw = { type: 'stack', key: 'root', children: [{ type: 'text', text: 'Hi' }, null, false, { type: 'badge', text: 'ok' }] }
    expect(ok(raw)).toEqual({
      type: 'stack', key: 'root', gap: 'md', children: [
        { type: 'text', text: 'Hi', tone: 'default', mono: false, size: 'md', weight: 'regular' },
        { type: 'badge', text: 'ok', tone: 'neutral' },
      ],
    })
  })

  it('refuses a field the node does not have, so a typo cannot vanish', () => {
    expect(problem({ type: 'stack', children: [{ type: 'text', text: 'a' }, null, { type: 'button', text: 'Go', action: 'go', varient: 'primary' }] }))
      .toBe('tree.children[2].varient is not a field of a "button" node.')
    expect(problem({ type: 'stack', childern: [] })).toBe('tree.childern is not a field of a "stack" node.')
    expect(problem({ type: 'button', text: 'Go', action: 'go', onClick: 'alert(1)' })).toBe('tree.onClick is not a field of a "button" node.')
    // The stray field is named before the missing one it was meant to be.
    expect(problem({ type: 'heading', title: 'Board' })).toBe('tree.title is not a field of a "heading" node.')
    expect(problem({ type: 'text', text: 'a', style: 'color:red' })).toBe('tree.style is not a field of a "text" node.')
    expect(problem({ type: 'divider', text: 'or' })).toBe('tree.text is not a field of a "divider" node.')
    expect(problem({ type: 'text', text: 'a', 'on click': 1 })).toBe('tree["on click"] is not a field of a "text" node.')
    // min, max and step belong to inputs, not textareas.
    expect(problem({ type: 'textarea', name: 'n', label: 'N', max: 3 })).toBe('tree.max is not a field of a "textarea" node.')
  })

  it('refuses unknown fields in nested records too', () => {
    const table = (column: object) => ({ type: 'table', columns: [column], rows: [] })
    expect(problem(table({ key: 'a', title: 'A' }))).toBe('tree.columns[0].title is not a field of a table column.')
    expect(problem({ type: 'metrics', items: [{ label: 'a', value: 1 }, { label: 'b', value: 2, hint: 'x' }] })).toBe('tree.items[1].hint is not a field of a metric.')
    expect(problem({ type: 'list', items: [{ title: 'a', subtitle: 'b' }] })).toBe('tree.items[0].subtitle is not a field of a list item.')
    expect(problem({ type: 'kv', items: [{ label: 'a', value: 1 }] })).toBe('tree.items[0].label is not a field of a kv item.')
    expect(problem({ type: 'select', name: 's', label: 'S', options: ['a', { value: 'b', text: 'B' }] })).toBe('tree.options[1].text is not a field of an option.')
    expect(problem({ type: 'segmented', name: 's', label: 'S', options: [{ value: 'a', disabled: true }] })).toBe('tree.options[0].disabled is not a field of an option.')
    expect(problem({ type: 'tabs', name: 't', tabs: [{ id: 'a', label: 'A', content: [] }] })).toBe('tree.tabs[0].content is not a field of a tab.')
    expect(problem({ type: 'panel', actions: [{ type: 'link', text: 'Docs', href: 'https://example.test', target: '_blank' }] })).toBe('tree.actions[0].target is not a field of a "link" node.')
    expect(problem({ type: 'callout', text: 'x', actions: [{ type: 'button', text: 'Fix', action: 'fix', onclick: 'x' }] })).toBe('tree.actions[0].onclick is not a field of a "button" node.')
  })

  it('keeps table rows open for the plugin’s own data, but refuses rows shaped unlike the columns', () => {
    // Extra row keys are the plugin's business: they are dropped, not refused.
    expect(ok({ type: 'table', columns: [{ key: 'name' }], rows: [{ id: 'n', name: 'North', region: 'eu', raw: { any: 'shape' } }] }))
      .toMatchObject({ rows: [{ id: 'n', name: 'North' }] })
    // One matching row is enough: the others are blank cells, not a shape mistake.
    expect(ok({ type: 'table', columns: [{ key: 'name' }, { key: 'bytes' }], rows: [{ id: 1 }, { id: 2, bytes: null }] }))
      .toMatchObject({ rows: [{ id: 1, name: null, bytes: null }, { id: 2, name: null, bytes: null }] })
    expect(problem({ type: 'table', columns: [{ key: 'name' }, { key: 'bytes' }], rows: [{ id: 'a', cells: { name: 'North', bytes: 1 } }, { id: 'b', cells: {} }] }))
      .toBe('tree.rows[0] has none of the column keys (name, bytes); rows are flat objects keyed by column key.')
    // A column named like an Object method reads only the row's own key.
    expect(problem({ type: 'table', columns: [{ key: 'toString' }], rows: [{ id: 1 }] })).toMatch(/^tree\.rows\[0\] has none of the column keys \(toString\)/u)
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
        { type: 'textarea', name: 'note', label: 'Note', rows: 6, action: 'save' },
        { type: 'tabs', name: 'tab', tabs: [{ id: 'one', label: 'One', children: [{ type: 'chart', label: 'Load', values: [1, 2, 3] }] }, { id: 'two', label: 'Two' }] },
        { type: 'callout', tone: 'warning', title: 'Heads up', text: 'Check this.', actions: [{ type: 'button', text: 'Fix', action: 'fix' }] },
      ],
    }
    const seen = new Set<string>()
    const walk = (node: UiNode) => {
      seen.add(node.type)
      if ('children' in node) node.children.forEach(walk)
      if (node.type === 'panel' || node.type === 'callout') node.actions.forEach(walk)
      if (node.type === 'tabs') node.tabs.forEach((tab) => tab.children.forEach(walk))
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

  it('keeps numbers and booleans as they are in metrics, kv lists and cells; a non-finite number becomes null', () => {
    expect(ok({ type: 'metric', label: 'Up', value: true })).toMatchObject({ value: true })
    expect(ok({ type: 'metric', label: 'Rate', value: 0.0004 })).toMatchObject({ value: 0.0004 })
    expect(ok({ type: 'metrics', items: [{ label: 'n', value: 1234567 }, { label: 'none', value: null }] })).toMatchObject({ items: [{ value: 1234567 }, { value: null }] })
    expect(ok({ type: 'kv', items: [{ key: 'n', value: Number.POSITIVE_INFINITY }, { key: 'm', value: 12.5 }] })).toMatchObject({ items: [{ key: 'n', value: null }, { key: 'm', value: 12.5 }] })
    expect(ok({ type: 'table', columns: [{ key: 'a' }, { key: 'b' }], rows: [{ a: 3.25, b: Number.NaN }] })).toMatchObject({ rows: [{ a: 3.25, b: null }] })
    expect(problem({ type: 'metric', label: 'x', value: {} })).toBe('tree.value must be a string.')
  })

  it('writes a number or boolean field value exactly as JavaScript spells it', () => {
    expect(ok({ type: 'input', name: 'n', label: 'N', kind: 'number', value: 1234.5 })).toMatchObject({ value: '1234.5' })
    expect(ok({ type: 'input', name: 'n', label: 'N', value: 0.0000001 })).toMatchObject({ value: '1e-7' })
    expect(ok({ type: 'input', name: 'n', label: 'N', value: false })).toMatchObject({ value: 'false' })
    expect(ok({ type: 'textarea', name: 'n', label: 'N', value: 1e21 })).toMatchObject({ value: '1e+21' })
    expect(problem({ type: 'input', name: 'n', label: 'N', value: Number.NaN })).toBe('tree.value must be a finite number.')
  })

  it('reports paths with the plugin’s own child indexes, skipped children included', () => {
    expect(problem({ type: 'stack', children: [null, false, { type: 'text', text: 'a' }, { type: 'text' }] })).toBe('tree.children[3].text must be a string.')
    expect(problem({ type: 'panel', actions: [false, { type: 'button', text: 'x', action: '' }] })).toMatch(/^tree\.actions\[1\]\.action must be 1–64/u)
  })

  it('refuses table rows that repeat an id, naming both rows', () => {
    const rows = Array.from({ length: 8 }, (_, index) => ({ id: index === 7 ? 'r2' : `r${index}`, a: index }))
    expect(problem({ type: 'table', columns: [{ key: 'a' }], rows })).toBe('tree.rows[7].id repeats the id of rows[2].')
    // 1 and "1" would be the same row in an event's eyes.
    expect(problem({ type: 'table', columns: [{ key: 'a' }], rows: [{ id: 1, a: 1 }, { id: '1', a: 2 }] })).toBe('tree.rows[1].id repeats the id of rows[0].')
    expect(problem({ type: 'table', columns: [{ key: 'a' }], rows: [{ id: true, a: 1 }] })).toBe('tree.rows[0].id must be a string or a finite number.')
    expect(ok({ type: 'table', columns: [{ key: 'a' }], rows: [{ id: null, a: null }, { id: null }] })).toMatchObject({ rows: [{ a: null }, { a: null }] })
  })

  it('counts characters, not UTF-16 units, against text limits', () => {
    const emoji = '\u{1F680}'
    expect(emoji.length).toBe(2)
    expect(ok({ type: 'heading', text: emoji.repeat(TREE_LIMITS.label) })).toMatchObject({ type: 'heading' })
    expect(problem({ type: 'heading', text: emoji.repeat(TREE_LIMITS.label + 1) })).toBe(`tree.text is longer than ${TREE_LIMITS.label} characters.`)
    expect(problem({ type: 'heading', text: 'a'.repeat(TREE_LIMITS.label + 1) })).toBe(`tree.text is longer than ${TREE_LIMITS.label} characters.`)
  })

  it('reads text fields: hint, error, required and number bounds', () => {
    expect(ok({ type: 'input', name: 'port', label: 'Port', kind: 'number', min: 1, max: 65535, step: 1, required: true, hint: '1–65535', error: 'Taken' })).toEqual({
      type: 'input', name: 'port', label: 'Port', hint: '1–65535', error: 'Taken', required: true, disabled: false,
      value: '', placeholder: '', kind: 'number', maxLength: 256, action: '', min: 1, max: 65535, step: 1,
    })
    expect(problem({ type: 'input', name: 'a', label: 'A', kind: 'number', min: 5, max: 1 })).toBe('tree.max must not be below min.')
    expect(problem({ type: 'input', name: 'a', label: 'A', kind: 'number', step: 0 })).toBe('tree.step must be above 0.')
    expect(problem({ type: 'input', name: 'a', label: 'A', kind: 'number', min: Number.NEGATIVE_INFINITY })).toBe('tree.min must be a finite number.')
    expect(problem({ type: 'input', name: 'a', label: 'A', min: 1 })).toBe('tree.min only applies to kind "number".')
    expect(problem({ type: 'input', name: 'a', label: 'A', error: 'x'.repeat(TREE_LIMITS.label + 1) })).toBe(`tree.error is longer than ${TREE_LIMITS.label} characters.`)
    expect(problem({ type: 'input', name: 'a', label: 'A', required: 'yes' })).toMatch(/^tree\.required must be/u)
  })

  it('reads textareas with bounded rows and length', () => {
    expect(ok({ type: 'textarea', name: 'note', label: 'Note' })).toEqual({
      type: 'textarea', name: 'note', label: 'Note', hint: '', error: '', required: false, disabled: false,
      value: '', placeholder: '', rows: 4, maxLength: 1000, action: '',
    })
    expect(problem({ type: 'textarea', name: 'n', label: 'N', rows: 1 })).toBe('tree.rows must be a whole number from 2 to 12.')
    expect(problem({ type: 'textarea', name: 'n', label: 'N', rows: 13 })).toBe('tree.rows must be a whole number from 2 to 12.')
    expect(problem({ type: 'textarea', name: 'n', label: 'N', maxLength: TREE_LIMITS.text + 1 })).toBe(`tree.maxLength must be a whole number from 1 to ${TREE_LIMITS.text}.`)
    expect(problem({ type: 'textarea', name: 'n', label: 'N', action: 'bad name' })).toMatch(/^tree\.action must be 1–64/u)
  })

  it('reads tabs: 1–8 unique ids, a value naming one of them, children checked per tab', () => {
    const tabs = ok({ type: 'tabs', name: 'view', tabs: [{ id: 'a', label: 'A' }, { id: 'b', label: 'B', children: [{ type: 'divider' }] }] })
    expect(tabs).toEqual({ type: 'tabs', name: 'view', label: '', value: 'a', tabs: [{ id: 'a', label: 'A', children: [] }, { id: 'b', label: 'B', children: [{ type: 'divider' }] }] })
    expect(problem({ type: 'tabs', name: 'v', tabs: [] })).toBe('tree.tabs needs at least one tab.')
    expect(problem({ type: 'tabs', name: 'v', tabs: Array.from({ length: 9 }, (_, index) => ({ id: `t${index}`, label: 'T' })) })).toBe('tree.tabs has 9 entries; at most 8.')
    expect(problem({ type: 'tabs', name: 'v', tabs: [{ id: 'a', label: 'A' }, { id: 'a', label: 'B' }] })).toBe('tree.tabs[1].id repeats the id of tabs[0].')
    expect(problem({ type: 'tabs', name: 'v', value: 'c', tabs: [{ id: 'a', label: 'A' }] })).toBe('tree.value must be the id of one of the tabs.')
    expect(problem({ type: 'tabs', name: 'v', tabs: [{ id: 'a b', label: 'A' }] })).toMatch(/^tree\.tabs\[0\]\.id must be 1–64/u)
    expect(problem({ type: 'tabs', name: 'v', tabs: [{ id: 'a', label: 'A', children: [null, { type: 'nope' }] }] })).toMatch(/^tree\.tabs\[0\]\.children\[1\]\.type "nope"/u)
  })

  it('reads charts: finite values, matching labels, bounded height and range', () => {
    expect(ok({ type: 'chart', label: 'Latency', values: [3, 1, 2] })).toEqual({ type: 'chart', kind: 'line', label: 'Latency', values: [3, 1, 2], labels: [], tone: 'info', height: 96 })
    expect(ok({ type: 'chart', kind: 'bars', label: 'L', values: [1], labels: ['Mon'], min: 0, max: 10, height: 48, tone: 'success' })).toMatchObject({ min: 0, max: 10, height: 48, labels: ['Mon'] })
    expect(problem({ type: 'chart', label: 'L', values: [] })).toBe('tree.values needs at least one value.')
    expect(problem({ type: 'chart', label: 'L', values: Array.from({ length: TREE_LIMITS.points + 1 }, () => 1) })).toBe(`tree.values has ${TREE_LIMITS.points + 1} entries; at most ${TREE_LIMITS.points}.`)
    expect(problem({ type: 'chart', label: 'L', values: [1, Number.NaN] })).toBe('tree.values[1] must be a finite number.')
    expect(problem({ type: 'chart', label: 'L', values: [1, '2'] })).toBe('tree.values[1] must be a finite number.')
    expect(problem({ type: 'chart', label: 'L', values: [1, 2], labels: ['a'] })).toBe('tree.labels has 1 entries; it needs one per value (2).')
    expect(problem({ type: 'chart', label: 'L', values: [1], height: 300 })).toBe('tree.height must be a whole number from 48 to 240.')
    expect(problem({ type: 'chart', label: 'L', values: [1], min: 5, max: 5 })).toBe('tree.max must be above min.')
    expect(problem({ type: 'chart', values: [1] })).toBe('tree.label must be a string.')
    expect(problem({ type: 'chart', label: 'L', kind: 'pie', values: [1] })).toMatch(/^tree\.kind must be one of line, bars/u)
  })

  it('charges chart values against the node budget', () => {
    const chart = { type: 'chart', label: 'L', values: Array.from({ length: TREE_LIMITS.points }, () => 1) }
    const charts = Array.from({ length: Math.ceil(TREE_LIMITS.nodes / TREE_LIMITS.points) }, () => chart)
    expect(problem({ type: 'stack', children: charts })).toMatch(/takes the page over 2000 nodes\.$/u)
  })

  it('reads callouts and indeterminate progress', () => {
    expect(ok({ type: 'callout', text: 'Saved.' })).toEqual({ type: 'callout', tone: 'info', title: '', text: 'Saved.', actions: [] })
    expect(problem({ type: 'callout', tone: 'loud', text: 'x' })).toMatch(/^tree\.tone must be one of neutral/u)
    expect(problem({ type: 'callout', title: 'x' })).toBe('tree.text must be a string.')
    expect(ok({ type: 'progress', indeterminate: true })).toEqual({ type: 'progress', value: 0, indeterminate: true, label: '', tone: 'info' })
    expect(problem({ type: 'progress' })).toBe('tree.value must be a number from 0 to 1.')
  })

  it('reads column formats and right-aligns figures by default', () => {
    const table = ok({ type: 'table', columns: [{ key: 'a' }, { key: 'b', format: 'bytes' }, { key: 'c', format: 'percent', align: 'left' }], rows: [] })
    expect(table).toMatchObject({ columns: [{ format: 'text', align: 'left' }, { format: 'bytes', align: 'right' }, { format: 'percent', align: 'left' }] })
    expect(problem({ type: 'table', columns: [{ key: 'a', format: 'money' }], rows: [] })).toMatch(/^tree\.columns\[0\]\.format must be one of text, number, percent, bytes, duration/u)
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

describe('formatScalar', () => {
  it('groups whole numbers and keeps four significant digits of a fraction, never an exponent', () => {
    expect(formatNumber(0)).toBe('0')
    expect(formatNumber(1234567)).toBe('1,234,567')
    expect(formatNumber(-42)).toBe('-42')
    expect(formatNumber(1.23456)).toBe('1.235')
    expect(formatNumber(0.0004)).toBe('0.0004')
    expect(formatNumber(0.00004)).toBe('0.00004')
    expect(formatNumber(1e-7)).toBe('0.0000001')
    expect(formatNumber(123456.789)).toBe('123,457')
    expect(formatNumber(1e21)).toBe('1,000,000,000,000,000,000,000')
  })

  it('writes booleans, null and strings plainly', () => {
    expect(formatScalar(true)).toBe('Yes')
    expect(formatScalar(false)).toBe('No')
    expect(formatScalar(null)).toBe('—')
    expect(formatScalar(undefined)).toBe('—')
    expect(formatScalar('12 ms')).toBe('12 ms')
    expect(formatScalar('12', 'bytes')).toBe('12')
  })

  it('applies column formats to numbers', () => {
    expect(formatScalar(0.1234, 'percent')).toBe('12.34%')
    expect(formatScalar(1, 'percent')).toBe('100%')
    expect(formatScalar(512, 'bytes')).toBe('512 B')
    expect(formatScalar(1536, 'bytes')).toBe('1.5 KiB')
    expect(formatScalar(5 * 1024 ** 3, 'bytes')).toBe('5 GiB')
    expect(formatScalar(250, 'duration')).toBe('250 ms')
    expect(formatScalar(1234, 'duration')).toBe('1.234 s')
    expect(formatScalar(125_000, 'duration')).toBe('2m 05s')
    expect(formatScalar(3_725_000, 'duration')).toBe('1h 02m')
    expect(formatScalar(1234.5, 'number')).toBe('1,235')
  })
})
