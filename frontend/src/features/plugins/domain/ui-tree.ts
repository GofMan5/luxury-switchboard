/**
 * The page vocabulary a plugin draws with. A plugin never touches the DOM:
 * it sends a tree of these nodes and the host renders them with the
 * Switchboard's own components, so every plugin page looks native, follows
 * the active theme and cannot inject markup or script into the window.
 *
 * The host validates each tree and copies only known properties, so the
 * renderer only ever sees the shapes declared here.
 */
export type Tone = 'neutral' | 'info' | 'success' | 'warning' | 'danger'
export type Gap = 'none' | 'sm' | 'md' | 'lg'
export type CellValue = string | number | boolean | null

interface Base { readonly key?: string }

export interface StackNode extends Base { readonly type: 'stack'; readonly gap: Gap; readonly children: readonly UiNode[] }
export interface RowNode extends Base { readonly type: 'row'; readonly gap: Gap; readonly align: 'start' | 'center' | 'end' | 'between'; readonly wrap: boolean; readonly children: readonly UiNode[] }
export interface GridNode extends Base { readonly type: 'grid'; readonly columns: number; readonly gap: Gap; readonly children: readonly UiNode[] }
export interface PanelNode extends Base { readonly type: 'panel'; readonly title: string; readonly subtitle: string; readonly actions: readonly UiNode[]; readonly children: readonly UiNode[] }
export interface HeadingNode extends Base { readonly type: 'heading'; readonly text: string; readonly level: 1 | 2 | 3 }
export interface TextNode extends Base { readonly type: 'text'; readonly text: string; readonly tone: Tone | 'muted' | 'default'; readonly mono: boolean; readonly size: 'sm' | 'md' | 'lg'; readonly weight: 'regular' | 'strong' }
export interface MetricItem { readonly label: string; readonly value: string; readonly detail: string; readonly tone?: 'success' | 'warning' | 'danger' }
export interface MetricNode extends Base, MetricItem { readonly type: 'metric' }
export interface MetricsNode extends Base { readonly type: 'metrics'; readonly items: readonly MetricItem[] }
export interface BadgeNode extends Base { readonly type: 'badge'; readonly text: string; readonly tone: Tone }
export interface DotNode extends Base { readonly type: 'dot'; readonly tone: Tone; readonly text: string }
export interface TableColumn { readonly key: string; readonly label: string; readonly align: 'left' | 'right'; readonly mono: boolean; readonly width: string }
export interface TableNode extends Base { readonly type: 'table'; readonly columns: readonly TableColumn[]; readonly rows: readonly Readonly<Record<string, CellValue>>[]; readonly empty: string; readonly rowAction: string }
export interface ListItem { readonly title: string; readonly detail: string; readonly meta: string; readonly tone?: Tone }
export interface ListNode extends Base { readonly type: 'list'; readonly items: readonly ListItem[]; readonly empty: string }
export interface CodeNode extends Base { readonly type: 'code'; readonly text: string }
export interface KvNode extends Base { readonly type: 'kv'; readonly items: readonly { readonly key: string; readonly value: string }[] }
export interface ButtonNode extends Base { readonly type: 'button'; readonly text: string; readonly action: string; readonly variant: 'default' | 'primary' | 'danger' | 'ghost'; readonly disabled: boolean; readonly icon: string }
export interface InputNode extends Base { readonly type: 'input'; readonly name: string; readonly label: string; readonly value: string; readonly placeholder: string; readonly kind: 'text' | 'number' | 'search'; readonly maxLength: number; readonly action: string; readonly disabled: boolean }
export interface Option { readonly value: string; readonly label: string }
export interface SelectNode extends Base { readonly type: 'select'; readonly name: string; readonly label: string; readonly value: string; readonly options: readonly Option[]; readonly disabled: boolean }
export interface ToggleNode extends Base { readonly type: 'toggle'; readonly name: string; readonly label: string; readonly value: boolean; readonly disabled: boolean }
export interface SegmentedNode extends Base { readonly type: 'segmented'; readonly name: string; readonly label: string; readonly value: string; readonly options: readonly Option[]; readonly disabled: boolean }
export interface ProgressNode extends Base { readonly type: 'progress'; readonly value: number; readonly label: string; readonly tone: Tone }
export interface DividerNode extends Base { readonly type: 'divider' }
export interface EmptyNode extends Base { readonly type: 'empty'; readonly title: string; readonly hint: string }
export interface LinkNode extends Base { readonly type: 'link'; readonly text: string; readonly href: string }

export type UiNode =
  | StackNode | RowNode | GridNode | PanelNode | HeadingNode | TextNode | MetricNode | MetricsNode
  | BadgeNode | DotNode | TableNode | ListNode | CodeNode | KvNode | ButtonNode | InputNode
  | SelectNode | ToggleNode | SegmentedNode | ProgressNode | DividerNode | EmptyNode | LinkNode

export type NodeType = UiNode['type']

export const NODE_TYPES: readonly NodeType[] = [
  'stack', 'row', 'grid', 'panel', 'heading', 'text', 'metric', 'metrics', 'badge', 'dot', 'table', 'list',
  'code', 'kv', 'button', 'input', 'select', 'toggle', 'segmented', 'progress', 'divider', 'empty', 'link',
]

export const TREE_LIMITS = {
  /** Nodes plus table rows, list items, options and metric items. */
  nodes: 2000,
  depth: 24,
  /** UTF-8 bytes of the tree's JSON. */
  bytes: 256 * 1024,
  text: 4096,
  code: 32 * 1024,
  label: 120,
  rows: 500,
  columns: 16,
  options: 100,
} as const

/** Field names and actions: what a plugin matches on in its event handler. */
const NAME_PATTERN = /^[A-Za-z0-9_.:-]{1,64}$/u

export type TreeCheck = { readonly ok: true; readonly tree: UiNode } | { readonly ok: false; readonly problem: string }

class TreeError extends Error {}

const TONES: readonly Tone[] = ['neutral', 'info', 'success', 'warning', 'danger']
const GAPS: readonly Gap[] = ['none', 'sm', 'md', 'lg']

/**
 * Checks a tree a plugin sent and returns a clean copy, or the first problem
 * with the path to it (`children[2].rows[14]`) so the author can find it.
 */
export function checkTree(raw: unknown): TreeCheck {
  let json: string
  try {
    json = JSON.stringify(raw) ?? ''
  } catch {
    return { ok: false, problem: 'The tree is not plain data (it has a cycle or a value JSON cannot hold).' }
  }
  const bytes = new TextEncoder().encode(json).length
  if (bytes > TREE_LIMITS.bytes) return { ok: false, problem: `The tree is ${Math.ceil(bytes / 1024)} KiB; a page takes at most ${TREE_LIMITS.bytes / 1024} KiB.` }
  const budget = { left: TREE_LIMITS.nodes }
  try {
    return { ok: true, tree: node(raw, 'tree', 0, budget) }
  } catch (error) {
    if (error instanceof TreeError) return { ok: false, problem: error.message }
    throw error
  }
}

function node(raw: unknown, path: string, depth: number, budget: { left: number }): UiNode {
  if (depth > TREE_LIMITS.depth) fail(path, `nests deeper than ${TREE_LIMITS.depth} levels`)
  spend(budget, 1, path)
  const value = record(raw, path)
  const type = value.type
  const key = value.key === undefined ? {} : { key: text(value.key, `${path}.key`, 64, false) }
  const at = (field: string) => `${path}.${field}`
  const kids = (field = 'children') => children(value[field], at(field), depth, budget)
  switch (type) {
    case 'stack': return { type, ...key, gap: oneOf(value.gap, GAPS, 'md', at('gap')), children: kids() }
    case 'row': return { type, ...key, gap: oneOf(value.gap, GAPS, 'md', at('gap')), align: oneOf(value.align, ['start', 'center', 'end', 'between'] as const, 'center', at('align')), wrap: bool(value.wrap, true, at('wrap')), children: kids() }
    case 'grid': return { type, ...key, columns: integer(value.columns, 1, 6, 2, at('columns')), gap: oneOf(value.gap, GAPS, 'md', at('gap')), children: kids() }
    case 'panel': return { type, ...key, title: optText(value.title, at('title'), TREE_LIMITS.label), subtitle: optText(value.subtitle, at('subtitle'), TREE_LIMITS.text), actions: kids('actions'), children: kids() }
    case 'heading': return { type, ...key, text: text(value.text, at('text'), TREE_LIMITS.label), level: integer(value.level, 1, 3, 2, at('level')) as 1 | 2 | 3 }
    case 'text': return {
      type, ...key, text: text(value.text, at('text'), TREE_LIMITS.text, true),
      tone: oneOf(value.tone, ['default', 'muted', ...TONES] as const, 'default', at('tone')),
      mono: bool(value.mono, false, at('mono')),
      size: oneOf(value.size, ['sm', 'md', 'lg'] as const, 'md', at('size')),
      weight: oneOf(value.weight, ['regular', 'strong'] as const, 'regular', at('weight')),
    }
    case 'metric': return { type, ...key, ...metric(value, path) }
    case 'metrics': {
      const items = array(value.items, at('items'), 12).map((item, index) => { spend(budget, 1, `${at('items')}[${index}]`); return metric(record(item, `${at('items')}[${index}]`), `${at('items')}[${index}]`) })
      return { type, ...key, items }
    }
    case 'badge': return { type, ...key, text: text(value.text, at('text'), TREE_LIMITS.label), tone: oneOf(value.tone, TONES, 'neutral', at('tone')) }
    case 'dot': return { type, ...key, tone: oneOf(value.tone, TONES, 'neutral', at('tone')), text: optText(value.text, at('text'), TREE_LIMITS.label) }
    case 'table': {
      const columns = array(value.columns, at('columns'), TREE_LIMITS.columns).map((column, index) => {
        const p = `${at('columns')}[${index}]`
        const c = record(column, p)
        return {
          key: name(c.key, `${p}.key`),
          label: optText(c.label, `${p}.label`, TREE_LIMITS.label),
          align: oneOf(c.align, ['left', 'right'] as const, 'left', `${p}.align`),
          mono: bool(c.mono, false, `${p}.mono`),
          width: width(c.width, `${p}.width`),
        }
      })
      if (columns.length === 0) fail(at('columns'), 'needs at least one column')
      const rows = array(value.rows, at('rows'), TREE_LIMITS.rows).map((row, index) => {
        const p = `${at('rows')}[${index}]`
        spend(budget, 1, p)
        const r = record(row, p)
        const clean: Record<string, CellValue> = {}
        for (const column of columns) clean[column.key] = cell(r[column.key], `${p}.${column.key}`)
        if (r.id !== undefined) clean.id = cell(r.id, `${p}.id`)
        return clean
      })
      return { type, ...key, columns, rows, empty: optText(value.empty, at('empty'), TREE_LIMITS.label) || 'Nothing to show yet.', rowAction: value.rowAction === undefined ? '' : name(value.rowAction, at('rowAction')) }
    }
    case 'list': {
      const items = array(value.items, at('items'), TREE_LIMITS.rows).map((item, index) => {
        const p = `${at('items')}[${index}]`
        spend(budget, 1, p)
        const i = record(item, p)
        return {
          title: text(i.title, `${p}.title`, TREE_LIMITS.text),
          detail: optText(i.detail, `${p}.detail`, TREE_LIMITS.text),
          meta: optText(i.meta, `${p}.meta`, TREE_LIMITS.label),
          ...(i.tone === undefined ? {} : { tone: oneOf(i.tone, TONES, 'neutral', `${p}.tone`) }),
        }
      })
      return { type, ...key, items, empty: optText(value.empty, at('empty'), TREE_LIMITS.label) || 'Nothing to show yet.' }
    }
    case 'code': return { type, ...key, text: text(value.text, at('text'), TREE_LIMITS.code, true) }
    case 'kv': {
      const items = array(value.items, at('items'), 64).map((item, index) => {
        const p = `${at('items')}[${index}]`
        spend(budget, 1, p)
        const i = record(item, p)
        return { key: text(i.key, `${p}.key`, TREE_LIMITS.label), value: scalarText(i.value, `${p}.value`) }
      })
      return { type, ...key, items }
    }
    case 'button': return {
      type, ...key, text: text(value.text, at('text'), TREE_LIMITS.label), action: name(value.action, at('action')),
      variant: oneOf(value.variant, ['default', 'primary', 'danger', 'ghost'] as const, 'default', at('variant')),
      disabled: bool(value.disabled, false, at('disabled')),
      icon: value.icon === undefined ? '' : name(value.icon, at('icon')),
    }
    case 'input': return {
      type, ...key, name: name(value.name, at('name')), label: text(value.label, at('label'), TREE_LIMITS.label),
      value: value.value === undefined ? '' : scalarText(value.value, at('value')),
      placeholder: optText(value.placeholder, at('placeholder'), TREE_LIMITS.label),
      kind: oneOf(value.kind, ['text', 'number', 'search'] as const, 'text', at('kind')),
      maxLength: integer(value.maxLength, 1, TREE_LIMITS.text, 256, at('maxLength')),
      action: value.action === undefined ? '' : name(value.action, at('action')),
      disabled: bool(value.disabled, false, at('disabled')),
    }
    case 'select':
    case 'segmented': {
      const options = array(value.options, at('options'), type === 'segmented' ? 6 : TREE_LIMITS.options).map((option, index) => {
        const p = `${at('options')}[${index}]`
        spend(budget, 1, p)
        if (typeof option === 'string') return { value: text(option, p, TREE_LIMITS.label), label: option }
        const o = record(option, p)
        const optionValue = text(o.value, `${p}.value`, TREE_LIMITS.label, true)
        return { value: optionValue, label: o.label === undefined ? optionValue : text(o.label, `${p}.label`, TREE_LIMITS.label) }
      })
      if (options.length === 0) fail(at('options'), 'needs at least one option')
      const selected = value.value === undefined ? options[0].value : text(value.value, at('value'), TREE_LIMITS.label, true)
      return { type, ...key, name: name(value.name, at('name')), label: text(value.label, at('label'), TREE_LIMITS.label), value: selected, options, disabled: bool(value.disabled, false, at('disabled')) }
    }
    case 'toggle': return { type, ...key, name: name(value.name, at('name')), label: text(value.label, at('label'), TREE_LIMITS.label), value: bool(value.value, false, at('value')), disabled: bool(value.disabled, false, at('disabled')) }
    case 'progress': {
      const amount = value.value
      if (typeof amount !== 'number' || !Number.isFinite(amount)) fail(at('value'), 'must be a number from 0 to 1')
      return { type, ...key, value: Math.min(1, Math.max(0, amount as number)), label: optText(value.label, at('label'), TREE_LIMITS.label), tone: oneOf(value.tone, TONES, 'info', at('tone')) }
    }
    case 'divider': return { type, ...key }
    case 'empty': return { type, ...key, title: text(value.title, at('title'), TREE_LIMITS.label), hint: optText(value.hint, at('hint'), TREE_LIMITS.text) }
    case 'link': {
      const href = text(value.href, at('href'), 2048)
      let url: URL | null = null
      try { url = new URL(href) } catch { /* reported below */ }
      if (!url || url.protocol !== 'https:' || url.username || url.password) fail(at('href'), 'must be an https:// address without credentials')
      return { type, ...key, text: text(value.text, at('text'), TREE_LIMITS.label), href }
    }
    default:
      return fail(at('type'), typeof type === 'string' ? `"${type.slice(0, 32)}" is not a node type (use one of ${NODE_TYPES.join(', ')})` : 'is missing')
  }
}

function metric(value: Record<string, unknown>, path: string): MetricItem {
  return {
    label: text(value.label, `${path}.label`, TREE_LIMITS.label),
    value: scalarText(value.value, `${path}.value`),
    detail: optText(value.detail, `${path}.detail`, TREE_LIMITS.label),
    ...(value.tone === undefined ? {} : { tone: oneOf(value.tone, ['success', 'warning', 'danger'] as const, 'success', `${path}.tone`) }),
  }
}

function children(raw: unknown, path: string, depth: number, budget: { left: number }): UiNode[] {
  if (raw === undefined) return []
  return array(raw, path, TREE_LIMITS.nodes).filter((child) => child !== null && child !== false).map((child, index) => node(child, `${path}[${index}]`, depth + 1, budget))
}

function spend(budget: { left: number }, amount: number, path: string): void {
  budget.left -= amount
  if (budget.left < 0) fail(path, `takes the page over ${TREE_LIMITS.nodes} nodes`)
}

function fail(path: string, message: string): never {
  throw new TreeError(`${path} ${message}.`)
}

function record(value: unknown, path: string): Record<string, unknown> {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) fail(path, 'must be an object')
  return value as Record<string, unknown>
}

function array(value: unknown, path: string, limit: number): unknown[] {
  if (!Array.isArray(value)) fail(path, 'must be a list')
  const list = value as unknown[]
  if (list.length > limit) fail(path, `has ${list.length} entries; at most ${limit}`)
  return list
}

function text(value: unknown, path: string, limit: number, allowEmpty = false): string {
  if (typeof value !== 'string') fail(path, 'must be a string')
  const result = value as string
  if (!allowEmpty && result.length === 0) fail(path, 'must not be empty')
  if (result.length > limit) fail(path, `is longer than ${limit} characters`)
  return result
}

function optText(value: unknown, path: string, limit: number): string {
  return value === undefined || value === null ? '' : text(value, path, limit, true)
}

/** Numbers and booleans read naturally as text in a metric or a cell. */
function scalarText(value: unknown, path: string): string {
  if (typeof value === 'number') return Number.isFinite(value) ? value.toLocaleString() : '—'
  if (typeof value === 'boolean') return value ? 'Yes' : 'No'
  if (value === null) return '—'
  return text(value, path, TREE_LIMITS.text, true)
}

function cell(value: unknown, path: string): CellValue {
  if (value === undefined || value === null) return null
  if (typeof value === 'number') return Number.isFinite(value) ? value : null
  if (typeof value === 'boolean') return value
  return text(value, path, TREE_LIMITS.text, true)
}

function name(value: unknown, path: string): string {
  if (typeof value !== 'string' || !NAME_PATTERN.test(value)) fail(path, 'must be 1–64 letters, digits, dots, colons, dashes or underscores')
  return value as string
}

function bool(value: unknown, fallback: boolean, path: string): boolean {
  if (value === undefined) return fallback
  if (typeof value !== 'boolean') fail(path, 'must be true or false')
  return value as boolean
}

function integer(value: unknown, min: number, max: number, fallback: number, path: string): number {
  if (value === undefined) return fallback
  if (typeof value !== 'number' || !Number.isInteger(value) || value < min || value > max) fail(path, `must be a whole number from ${min} to ${max}`)
  return value as number
}

function oneOf<const T extends string>(value: unknown, allowed: readonly T[], fallback: T, path: string): T {
  if (value === undefined) return fallback
  if (typeof value !== 'string' || !allowed.includes(value as T)) fail(path, `must be one of ${allowed.join(', ')}`)
  return value as T
}

const WIDTH = /^(?:\d{1,4}px|\d{1,3}%|auto)$/u
function width(value: unknown, path: string): string {
  if (value === undefined) return 'auto'
  if (typeof value === 'number' && Number.isInteger(value) && value > 0 && value <= 2000) return `${value}px`
  if (typeof value !== 'string' || !WIDTH.test(value)) fail(path, 'must be a pixel number, "120px", "20%" or "auto"')
  return value as string
}
