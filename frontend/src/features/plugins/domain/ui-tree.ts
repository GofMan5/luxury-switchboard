/**
 * The page vocabulary a plugin draws with. A plugin never touches the DOM:
 * it sends a tree of these nodes and the host renders them with the
 * Switchboard's own components, so every plugin page looks native, follows
 * the active theme and cannot inject markup or script into the window.
 *
 * The host validates each tree, refuses fields a shape does not have and
 * copies only known properties, so the renderer only ever sees the shapes
 * declared here. Table rows are the one open record (see the `table` case).
 */
import { CANVAS_LIMITS } from './canvas'

export type Tone = 'neutral' | 'info' | 'success' | 'warning' | 'danger'
export type Gap = 'none' | 'sm' | 'md' | 'lg'
/** A number stays a number so the window formats it once, the same way everywhere. */
export type Scalar = string | number | boolean | null
export type CellValue = Scalar
export type ColumnFormat = 'text' | 'number' | 'percent' | 'bytes' | 'duration'

interface Base { readonly key?: string }

/** What every labelled form field shares. `error` marks the field invalid and is read out with it. */
interface FieldBase extends Base { readonly name: string; readonly label: string; readonly hint: string; readonly error: string; readonly required: boolean; readonly disabled: boolean }

export interface StackNode extends Base { readonly type: 'stack'; readonly gap: Gap; readonly children: readonly UiNode[] }
export interface RowNode extends Base { readonly type: 'row'; readonly gap: Gap; readonly align: 'start' | 'center' | 'end' | 'between'; readonly wrap: boolean; readonly children: readonly UiNode[] }
export interface GridNode extends Base { readonly type: 'grid'; readonly columns: number; readonly gap: Gap; readonly children: readonly UiNode[] }
export interface PanelNode extends Base { readonly type: 'panel'; readonly title: string; readonly subtitle: string; readonly actions: readonly UiNode[]; readonly children: readonly UiNode[] }
export interface HeadingNode extends Base { readonly type: 'heading'; readonly text: string; readonly level: 1 | 2 | 3 }
export interface TextNode extends Base { readonly type: 'text'; readonly text: string; readonly tone: Tone | 'muted' | 'default'; readonly mono: boolean; readonly size: 'sm' | 'md' | 'lg'; readonly weight: 'regular' | 'strong' }
export interface MetricItem { readonly label: string; readonly value: Scalar; readonly detail: string; readonly tone?: 'success' | 'warning' | 'danger' }
export interface MetricNode extends Base, MetricItem { readonly type: 'metric' }
export interface MetricsNode extends Base { readonly type: 'metrics'; readonly items: readonly MetricItem[] }
export interface BadgeNode extends Base { readonly type: 'badge'; readonly text: string; readonly tone: Tone }
export interface DotNode extends Base { readonly type: 'dot'; readonly tone: Tone; readonly text: string }
export interface TableColumn { readonly key: string; readonly label: string; readonly align: 'left' | 'right'; readonly mono: boolean; readonly width: string; readonly format: ColumnFormat }
export interface TableNode extends Base { readonly type: 'table'; readonly columns: readonly TableColumn[]; readonly rows: readonly Readonly<Record<string, CellValue>>[]; readonly empty: string; readonly rowAction: string }
export interface ListItem { readonly title: string; readonly detail: string; readonly meta: string; readonly tone?: Tone }
export interface ListNode extends Base { readonly type: 'list'; readonly items: readonly ListItem[]; readonly empty: string }
export interface CodeNode extends Base { readonly type: 'code'; readonly text: string }
export interface KvItem { readonly key: string; readonly value: Scalar }
export interface KvNode extends Base { readonly type: 'kv'; readonly items: readonly KvItem[] }
export interface ButtonNode extends Base { readonly type: 'button'; readonly text: string; readonly action: string; readonly variant: 'default' | 'primary' | 'danger' | 'ghost'; readonly disabled: boolean; readonly icon: string }
export interface InputNode extends FieldBase {
  readonly type: 'input'; readonly value: string; readonly placeholder: string; readonly kind: 'text' | 'number' | 'search'
  readonly maxLength: number; readonly action: string
  /** Only for `kind: 'number'`. */
  readonly min?: number; readonly max?: number; readonly step?: number
}
export interface TextareaNode extends FieldBase { readonly type: 'textarea'; readonly value: string; readonly placeholder: string; readonly rows: number; readonly maxLength: number; readonly action: string }
export interface Option { readonly value: string; readonly label: string }
export interface SelectNode extends Base { readonly type: 'select'; readonly name: string; readonly label: string; readonly value: string; readonly options: readonly Option[]; readonly disabled: boolean }
export interface ToggleNode extends Base { readonly type: 'toggle'; readonly name: string; readonly label: string; readonly value: boolean; readonly disabled: boolean }
export interface SegmentedNode extends Base { readonly type: 'segmented'; readonly name: string; readonly label: string; readonly value: string; readonly options: readonly Option[]; readonly disabled: boolean }
export interface Tab { readonly id: string; readonly label: string; readonly children: readonly UiNode[] }
export interface TabsNode extends Base { readonly type: 'tabs'; readonly name: string; readonly label: string; readonly value: string; readonly tabs: readonly Tab[] }
export interface ProgressNode extends Base { readonly type: 'progress'; readonly value: number; readonly indeterminate: boolean; readonly label: string; readonly tone: Tone }
export interface ChartNode extends Base {
  readonly type: 'chart'; readonly kind: 'line' | 'bars'; readonly label: string; readonly values: readonly number[]; readonly labels: readonly string[]
  readonly tone: Tone; readonly height: number; readonly min?: number; readonly max?: number
}
export interface CalloutNode extends Base { readonly type: 'callout'; readonly tone: Tone; readonly title: string; readonly text: string; readonly actions: readonly UiNode[] }
export interface DividerNode extends Base { readonly type: 'divider' }
export interface EmptyNode extends Base { readonly type: 'empty'; readonly title: string; readonly hint: string }
export interface LinkNode extends Base { readonly type: 'link'; readonly text: string; readonly href: string }
/** Formatted text; the window parses it into elements (see `markdown.ts`), never into HTML. */
export interface MarkdownNode extends Base { readonly type: 'markdown'; readonly text: string }
/** An embedded picture: `src` is always a checked `data:image/…;base64,` URL, never a remote address. */
export interface ImageNode extends Base { readonly type: 'image'; readonly src: string; readonly alt: string; readonly width?: number; readonly height?: number }
/** `value` sits on a step inside the range, so what the window shows is what the form holds. */
export interface SliderNode extends Base { readonly type: 'slider'; readonly name: string; readonly label: string; readonly min: number; readonly max: number; readonly step: number; readonly value: number; readonly disabled: boolean }
/** `value` is lower-case `#rrggbb`, the form a colour input reports. */
export interface ColorNode extends Base { readonly type: 'color'; readonly name: string; readonly label: string; readonly value: string; readonly disabled: boolean }
export interface DetailsNode extends Base { readonly type: 'details'; readonly summary: string; readonly open: boolean; readonly children: readonly UiNode[] }
/** A drawing surface the plugin paints with `ui.draw`; `id` is unique in its tree. */
export interface CanvasNode extends Base {
  readonly type: 'canvas'; readonly id: string; readonly width: number; readonly height: number; readonly label: string
  readonly keys: boolean; readonly pointer: boolean; readonly frames: boolean; readonly fit: 'contain' | 'none'
}

export type UiNode =
  | StackNode | RowNode | GridNode | PanelNode | HeadingNode | TextNode | MetricNode | MetricsNode
  | BadgeNode | DotNode | TableNode | ListNode | CodeNode | KvNode | ButtonNode | InputNode | TextareaNode
  | SelectNode | ToggleNode | SegmentedNode | TabsNode | ProgressNode | ChartNode | CalloutNode
  | DividerNode | EmptyNode | LinkNode
  | MarkdownNode | ImageNode | SliderNode | ColorNode | DetailsNode | CanvasNode

export type NodeType = UiNode['type']

export const NODE_TYPES: readonly NodeType[] = [
  'stack', 'row', 'grid', 'panel', 'heading', 'text', 'metric', 'metrics', 'badge', 'dot', 'table', 'list',
  'code', 'kv', 'button', 'input', 'textarea', 'select', 'toggle', 'segmented', 'tabs', 'progress', 'chart',
  'callout', 'divider', 'empty', 'link', 'markdown', 'image', 'slider', 'color', 'details', 'canvas',
]

export const TREE_LIMITS = {
  /** Nodes plus table rows, list items, options, tabs, metric items and chart values. */
  nodes: 2000,
  depth: 24,
  /** UTF-8 bytes of the tree's JSON. */
  bytes: 256 * 1024,
  /** Lengths count characters (code points), not UTF-16 units. */
  text: 4096,
  code: 32 * 1024,
  label: 120,
  rows: 500,
  columns: 16,
  options: 100,
  tabs: 8,
  points: 200,
  markdown: 20_000,
  /** Characters of an image's `data:` URL, which is all ASCII, so characters are bytes. */
  image: 256 * 1024,
} as const

/** Field names and actions: what a plugin matches on in its event handler. */
const NAME_PATTERN = /^[A-Za-z0-9_.:-]{1,64}$/u

export type TreeCheck = { readonly ok: true; readonly tree: UiNode } | { readonly ok: false; readonly problem: string }

class TreeError extends Error {}

const TONES: readonly Tone[] = ['neutral', 'info', 'success', 'warning', 'danger']
const GAPS: readonly Gap[] = ['none', 'sm', 'md', 'lg']
const COLUMN_FORMATS: readonly ColumnFormat[] = ['text', 'number', 'percent', 'bytes', 'duration']

/** Every fixed-shape record a tree holds: each node type (less `type` and `key`) and the records nested in nodes. */
type Shapes = { readonly [T in NodeType]: Omit<Extract<UiNode, { type: T }>, 'type' | 'key'> } & {
  readonly column: TableColumn; readonly listItem: ListItem; readonly kvItem: KvItem; readonly option: Option; readonly tab: Tab
}

/**
 * The fields each shape takes; nodes also take `type` and `key`. Anything else
 * is refused: a misspelt `varient` or `childern` must not vanish and leave a
 * page that half works. Table rows are deliberately not listed (see the `table` case).
 */
const FIELD_LISTS = {
  stack: ['gap', 'children'],
  row: ['gap', 'align', 'wrap', 'children'],
  grid: ['columns', 'gap', 'children'],
  panel: ['title', 'subtitle', 'actions', 'children'],
  heading: ['text', 'level'],
  text: ['text', 'tone', 'mono', 'size', 'weight'],
  metric: ['label', 'value', 'detail', 'tone'],
  metrics: ['items'],
  badge: ['text', 'tone'],
  dot: ['tone', 'text'],
  table: ['columns', 'rows', 'empty', 'rowAction'],
  list: ['items', 'empty'],
  code: ['text'],
  kv: ['items'],
  button: ['text', 'action', 'variant', 'disabled', 'icon'],
  input: ['name', 'label', 'hint', 'error', 'required', 'disabled', 'value', 'placeholder', 'kind', 'maxLength', 'action', 'min', 'max', 'step'],
  textarea: ['name', 'label', 'hint', 'error', 'required', 'disabled', 'value', 'placeholder', 'rows', 'maxLength', 'action'],
  select: ['name', 'label', 'value', 'options', 'disabled'],
  toggle: ['name', 'label', 'value', 'disabled'],
  segmented: ['name', 'label', 'value', 'options', 'disabled'],
  tabs: ['name', 'label', 'value', 'tabs'],
  progress: ['value', 'indeterminate', 'label', 'tone'],
  chart: ['kind', 'label', 'values', 'labels', 'min', 'max', 'tone', 'height'],
  callout: ['tone', 'title', 'text', 'actions'],
  divider: [],
  empty: ['title', 'hint'],
  link: ['text', 'href'],
  markdown: ['text'],
  image: ['src', 'alt', 'width', 'height'],
  slider: ['name', 'label', 'min', 'max', 'step', 'value', 'disabled'],
  color: ['name', 'label', 'value', 'disabled'],
  details: ['summary', 'open', 'children'],
  canvas: ['id', 'width', 'height', 'label', 'keys', 'pointer', 'frames', 'fit'],
  column: ['key', 'label', 'align', 'mono', 'width', 'format'],
  listItem: ['title', 'detail', 'meta', 'tone'],
  kvItem: ['key', 'value'],
  option: ['value', 'label'],
  tab: ['id', 'label', 'children'],
} as const satisfies { readonly [S in keyof Shapes]: readonly (keyof Shapes[S])[] }

type Unlisted = { [S in keyof Shapes]: Exclude<keyof Shapes[S], (typeof FIELD_LISTS)[S][number]> }[keyof Shapes]
/** Checked both ways: each listed field exists, and a field added to a shape without listing it here does not compile. */
const FIELDS: typeof FIELD_LISTS & ([Unlisted] extends [never] ? unknown : { readonly unlisted: Unlisted }) = FIELD_LISTS

const INTEGER = new Intl.NumberFormat('en-US', { maximumFractionDigits: 0 })
const FRACTION = new Intl.NumberFormat('en-US', { maximumSignificantDigits: 4 })

/**
 * How the window writes a number: grouped whole numbers (`12,345`), four
 * significant digits for fractions (`1.235`, `0.00004`) and never an exponent,
 * so a small non-zero value never reads as `0`. Above a thousand a fraction is
 * rounded to whole units (`123,457`) instead of losing digits.
 */
export function formatNumber(value: number): string {
  if (!Number.isFinite(value)) return '—'
  if (value === 0) return '0'
  if (Number.isInteger(value) || Math.abs(value) >= 1000) return INTEGER.format(value)
  return FRACTION.format(value)
}

const BYTE_UNITS = ['KiB', 'MiB', 'GiB', 'TiB'] as const

/** A metric, kv or table value as text: the one formatter every plugin value goes through. */
export function formatScalar(value: Scalar | undefined, format: ColumnFormat = 'text'): string {
  if (value === null || value === undefined) return '—'
  if (typeof value === 'boolean') return value ? 'Yes' : 'No'
  if (typeof value === 'string') return value
  if (!Number.isFinite(value)) return '—'
  switch (format) {
    case 'percent': return `${formatNumber(value * 100)}%`
    case 'bytes': {
      let amount = Math.abs(value)
      if (amount < 1024) return `${formatNumber(value)} B`
      let unit = -1
      while (amount >= 1024 && unit < BYTE_UNITS.length - 1) { amount /= 1024; unit += 1 }
      return `${value < 0 ? '-' : ''}${FRACTION.format(amount)} ${BYTE_UNITS[unit]}`
    }
    case 'duration': {
      const ms = Math.abs(value)
      const sign = value < 0 ? '-' : ''
      if (ms < 1000) return `${sign}${formatNumber(ms)} ms`
      if (ms < 60_000) return `${sign}${FRACTION.format(ms / 1000)} s`
      const seconds = Math.round(ms / 1000)
      const minutes = Math.floor(seconds / 60)
      if (minutes < 60) return `${sign}${minutes}m ${String(seconds % 60).padStart(2, '0')}s`
      return `${sign}${INTEGER.format(Math.floor(minutes / 60))}h ${String(minutes % 60).padStart(2, '0')}m`
    }
    default: return formatNumber(value)
  }
}

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
  const budget: Walk = { left: TREE_LIMITS.nodes, canvases: new Map() }
  try {
    return { ok: true, tree: node(raw, 'tree', 0, budget) }
  } catch (error) {
    if (error instanceof TreeError) return { ok: false, problem: error.message }
    throw error
  }
}

/** What one check carries through the tree: the node budget left and where each canvas id first appeared. */
interface Walk { left: number; readonly canvases: Map<string, string> }

function node(raw: unknown, path: string, depth: number, budget: Walk): UiNode {
  if (depth > TREE_LIMITS.depth) fail(path, `nests deeper than ${TREE_LIMITS.depth} levels`)
  spend(budget, 1, path)
  const value = record(raw, path)
  const type = value.type
  // Before anything else, so `{ type: 'heading', title }` names the stray field, not the missing `text`.
  if (isNodeType(type)) only(value, ['type', 'key', ...FIELDS[type]], path, `a "${type}" node`)
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
      const items = array(value.items, at('items'), 12).map((item, index) => {
        const p = `${at('items')}[${index}]`
        spend(budget, 1, p)
        return metric(fields(item, FIELDS.metric, p, 'a metric'), p)
      })
      return { type, ...key, items }
    }
    case 'badge': return { type, ...key, text: text(value.text, at('text'), TREE_LIMITS.label), tone: oneOf(value.tone, TONES, 'neutral', at('tone')) }
    case 'dot': return { type, ...key, tone: oneOf(value.tone, TONES, 'neutral', at('tone')), text: optText(value.text, at('text'), TREE_LIMITS.label) }
    case 'table': {
      const columns = array(value.columns, at('columns'), TREE_LIMITS.columns).map((column, index) => {
        const p = `${at('columns')}[${index}]`
        const c = fields(column, FIELDS.column, p, 'a table column')
        const format = oneOf(c.format, COLUMN_FORMATS, 'text', `${p}.format`)
        return {
          key: name(c.key, `${p}.key`),
          label: optText(c.label, `${p}.label`, TREE_LIMITS.label),
          // Figures line up on the right unless the plugin says otherwise.
          align: oneOf(c.align, ['left', 'right'] as const, format === 'text' ? 'left' : 'right', `${p}.align`),
          mono: bool(c.mono, false, `${p}.mono`),
          width: width(c.width, `${p}.width`),
          format,
        }
      })
      if (columns.length === 0) fail(at('columns'), 'needs at least one column')
      // Rows are the one open record: a plugin may keep its own data next to the
      // column keys and `id` (a row straight from an API, say). Those extra keys
      // are ignored by design and never reach the window, so they are not refused.
      // Ids name rows in events and keep them stable on screen, so one id is one row.
      const seen = new Map<string, number>()
      let matched = false
      const rows = array(value.rows, at('rows'), TREE_LIMITS.rows).map((row, index) => {
        const p = `${at('rows')}[${index}]`
        spend(budget, 1, p)
        const r = record(row, p)
        const clean: Record<string, CellValue> = {}
        for (const column of columns) {
          // Own keys only: a column named `toString` must not read the prototype.
          const own = Object.hasOwn(r, column.key)
          if (own) matched = true
          clean[column.key] = cell(own ? r[column.key] : undefined, `${p}.${column.key}`)
        }
        if (r.id !== undefined && r.id !== null) {
          const id = rowId(r.id, `${p}.id`)
          const first = seen.get(String(id))
          if (first !== undefined) fail(`${p}.id`, `repeats the id of rows[${first}]`)
          seen.set(String(id), index)
          clean.id = id
        }
        return clean
      })
      // Rows that share no key with the columns are a shape mistake (`{ id, cells: {…} }`), not a table of blanks.
      if (rows.length > 0 && !matched) fail(`${at('rows')}[0]`, `has none of the column keys (${columns.map((column) => column.key).join(', ')}); rows are flat objects keyed by column key`)
      return { type, ...key, columns, rows, empty: optText(value.empty, at('empty'), TREE_LIMITS.label) || 'Nothing to show yet.', rowAction: value.rowAction === undefined ? '' : name(value.rowAction, at('rowAction')) }
    }
    case 'list': {
      const items = array(value.items, at('items'), TREE_LIMITS.rows).map((item, index) => {
        const p = `${at('items')}[${index}]`
        spend(budget, 1, p)
        const i = fields(item, FIELDS.listItem, p, 'a list item')
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
        const i = fields(item, FIELDS.kvItem, p, 'a kv item')
        return { key: text(i.key, `${p}.key`, TREE_LIMITS.label), value: scalar(i.value, `${p}.value`) }
      })
      return { type, ...key, items }
    }
    case 'button': return {
      type, ...key, text: text(value.text, at('text'), TREE_LIMITS.label), action: name(value.action, at('action')),
      variant: oneOf(value.variant, ['default', 'primary', 'danger', 'ghost'] as const, 'default', at('variant')),
      disabled: bool(value.disabled, false, at('disabled')),
      icon: value.icon === undefined ? '' : name(value.icon, at('icon')),
    }
    case 'input': {
      const kind = oneOf(value.kind, ['text', 'number', 'search'] as const, 'text', at('kind'))
      const bounds = { min: finite(value.min, at('min')), max: finite(value.max, at('max')), step: finite(value.step, at('step')) }
      if (kind !== 'number') {
        for (const bound of ['min', 'max', 'step'] as const) if (bounds[bound] !== undefined) fail(at(bound), 'only applies to kind "number"')
      }
      if (bounds.min !== undefined && bounds.max !== undefined && bounds.min > bounds.max) fail(at('max'), 'must not be below min')
      if (bounds.step !== undefined && bounds.step <= 0) fail(at('step'), 'must be above 0')
      return {
        type, ...key, ...field(value, at),
        value: fieldValue(value.value, at('value')),
        placeholder: optText(value.placeholder, at('placeholder'), TREE_LIMITS.label),
        kind,
        maxLength: integer(value.maxLength, 1, TREE_LIMITS.text, 256, at('maxLength')),
        action: value.action === undefined ? '' : name(value.action, at('action')),
        ...(bounds.min === undefined ? {} : { min: bounds.min }),
        ...(bounds.max === undefined ? {} : { max: bounds.max }),
        ...(bounds.step === undefined ? {} : { step: bounds.step }),
      }
    }
    case 'textarea': return {
      type, ...key, ...field(value, at),
      value: fieldValue(value.value, at('value')),
      placeholder: optText(value.placeholder, at('placeholder'), TREE_LIMITS.label),
      rows: integer(value.rows, 2, 12, 4, at('rows')),
      maxLength: integer(value.maxLength, 1, TREE_LIMITS.text, 1000, at('maxLength')),
      action: value.action === undefined ? '' : name(value.action, at('action')),
    }
    case 'select':
    case 'segmented': {
      const options = array(value.options, at('options'), type === 'segmented' ? 6 : TREE_LIMITS.options).map((option, index) => {
        const p = `${at('options')}[${index}]`
        spend(budget, 1, p)
        if (typeof option === 'string') return { value: text(option, p, TREE_LIMITS.label), label: option }
        const o = fields(option, FIELDS.option, p, 'an option')
        const optionValue = text(o.value, `${p}.value`, TREE_LIMITS.label, true)
        return { value: optionValue, label: o.label === undefined ? optionValue : text(o.label, `${p}.label`, TREE_LIMITS.label) }
      })
      if (options.length === 0) fail(at('options'), 'needs at least one option')
      const selected = value.value === undefined ? options[0].value : text(value.value, at('value'), TREE_LIMITS.label, true)
      return { type, ...key, name: name(value.name, at('name')), label: text(value.label, at('label'), TREE_LIMITS.label), value: selected, options, disabled: bool(value.disabled, false, at('disabled')) }
    }
    case 'toggle': return { type, ...key, name: name(value.name, at('name')), label: text(value.label, at('label'), TREE_LIMITS.label), value: bool(value.value, false, at('value')), disabled: bool(value.disabled, false, at('disabled')) }
    case 'tabs': {
      const seen = new Map<string, number>()
      const tabs = array(value.tabs, at('tabs'), TREE_LIMITS.tabs).map((tab, index) => {
        const p = `${at('tabs')}[${index}]`
        spend(budget, 1, p)
        const t = fields(tab, FIELDS.tab, p, 'a tab')
        const id = name(t.id, `${p}.id`)
        const first = seen.get(id)
        if (first !== undefined) fail(`${p}.id`, `repeats the id of tabs[${first}]`)
        seen.set(id, index)
        return { id, label: text(t.label, `${p}.label`, TREE_LIMITS.label), children: children(t.children, `${p}.children`, depth, budget) }
      })
      if (tabs.length === 0) fail(at('tabs'), 'needs at least one tab')
      const selected = value.value === undefined ? tabs[0].id : name(value.value, at('value'))
      if (!seen.has(selected)) fail(at('value'), 'must be the id of one of the tabs')
      return { type, ...key, name: name(value.name, at('name')), label: optText(value.label, at('label'), TREE_LIMITS.label), value: selected, tabs }
    }
    case 'progress': {
      const indeterminate = bool(value.indeterminate, false, at('indeterminate'))
      const amount = indeterminate && value.value === undefined ? 0 : value.value
      if (typeof amount !== 'number' || !Number.isFinite(amount)) fail(at('value'), 'must be a number from 0 to 1')
      return { type, ...key, value: Math.min(1, Math.max(0, amount as number)), indeterminate, label: optText(value.label, at('label'), TREE_LIMITS.label), tone: oneOf(value.tone, TONES, 'info', at('tone')) }
    }
    case 'chart': {
      const raw = array(value.values, at('values'), TREE_LIMITS.points)
      if (raw.length === 0) fail(at('values'), 'needs at least one value')
      spend(budget, raw.length, at('values'))
      const values = raw.map((point, index) => {
        if (typeof point !== 'number' || !Number.isFinite(point)) fail(`${at('values')}[${index}]`, 'must be a finite number')
        return point as number
      })
      const labels = value.labels === undefined ? [] : array(value.labels, at('labels'), TREE_LIMITS.points).map((label, index) => text(label, `${at('labels')}[${index}]`, TREE_LIMITS.label, true))
      if (value.labels !== undefined && labels.length !== values.length) fail(at('labels'), `has ${labels.length} entries; it needs one per value (${values.length})`)
      const min = finite(value.min, at('min'))
      const max = finite(value.max, at('max'))
      if (min !== undefined && max !== undefined && max <= min) fail(at('max'), 'must be above min')
      return {
        type, ...key, kind: oneOf(value.kind, ['line', 'bars'] as const, 'line', at('kind')),
        label: text(value.label, at('label'), TREE_LIMITS.label), values, labels,
        tone: oneOf(value.tone, TONES, 'info', at('tone')), height: integer(value.height, 48, 240, 96, at('height')),
        ...(min === undefined ? {} : { min }), ...(max === undefined ? {} : { max }),
      }
    }
    case 'callout': return {
      type, ...key, tone: oneOf(value.tone, TONES, 'info', at('tone')),
      title: optText(value.title, at('title'), TREE_LIMITS.label), text: text(value.text, at('text'), TREE_LIMITS.text),
      actions: kids('actions'),
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
    case 'markdown': return { type, ...key, text: text(value.text, at('text'), TREE_LIMITS.markdown, true) }
    case 'image': {
      if (typeof value.src !== 'string') fail(at('src'), 'must be a string')
      const src = value.src as string
      if (src.length > TREE_LIMITS.image) fail(at('src'), `is ${Math.ceil(src.length / 1024)} KiB; an image takes at most ${TREE_LIMITS.image / 1024} KiB`)
      if (!IMAGE_SOURCE.test(src)) fail(at('src'), 'must be a data:image/png, jpeg, gif or webp;base64, URL (remote addresses are refused)')
      const width = value.width === undefined ? undefined : integer(value.width, 1, CANVAS_LIMITS.size, 0, at('width'))
      const height = value.height === undefined ? undefined : integer(value.height, 1, CANVAS_LIMITS.size, 0, at('height'))
      return {
        type, ...key, src, alt: text(value.alt, at('alt'), TREE_LIMITS.label),
        ...(width === undefined ? {} : { width }), ...(height === undefined ? {} : { height }),
      }
    }
    case 'slider': {
      const min = number(value.min, at('min'))
      const max = number(value.max, at('max'))
      const step = number(value.step, at('step'))
      if (max <= min) fail(at('max'), 'must be above min')
      if (!Number.isFinite(max - min)) fail(at('max'), 'is too far from min')
      if (step <= 0) fail(at('step'), 'must be above 0')
      const amount = number(value.value, at('value'))
      return {
        type, ...key, name: name(value.name, at('name')), label: text(value.label, at('label'), TREE_LIMITS.label),
        min, max, step, value: onStep(amount, min, max, step), disabled: bool(value.disabled, false, at('disabled')),
      }
    }
    case 'color': {
      if (typeof value.value !== 'string' || !HEX_COLOR.test(value.value)) fail(at('value'), 'must be a #rrggbb colour')
      return {
        type, ...key, name: name(value.name, at('name')), label: text(value.label, at('label'), TREE_LIMITS.label),
        value: (value.value as string).toLowerCase(), disabled: bool(value.disabled, false, at('disabled')),
      }
    }
    case 'details': return { type, ...key, summary: text(value.summary, at('summary'), TREE_LIMITS.label), open: bool(value.open, false, at('open')), children: kids() }
    case 'canvas': {
      const id = name(value.id, at('id'))
      const first = budget.canvases.get(id)
      if (first !== undefined) fail(at('id'), `repeats the id of the canvas at ${first}`)
      budget.canvases.set(id, path)
      return {
        type, ...key, id,
        width: integer(needed(value.width, at('width')), 1, CANVAS_LIMITS.size, 0, at('width')),
        height: integer(needed(value.height, at('height')), 1, CANVAS_LIMITS.size, 0, at('height')),
        label: text(value.label, at('label'), TREE_LIMITS.label),
        keys: bool(value.keys, false, at('keys')), pointer: bool(value.pointer, false, at('pointer')), frames: bool(value.frames, false, at('frames')),
        fit: oneOf(value.fit, ['contain', 'none'] as const, 'contain', at('fit')),
      }
    }
    default:
      return fail(at('type'), typeof type === 'string' ? `"${type.slice(0, 32)}" is not a node type (use one of ${NODE_TYPES.join(', ')})` : 'is missing')
  }
}

function metric(value: Record<string, unknown>, path: string): MetricItem {
  return {
    label: text(value.label, `${path}.label`, TREE_LIMITS.label),
    value: scalar(value.value, `${path}.value`),
    detail: optText(value.detail, `${path}.detail`, TREE_LIMITS.label),
    ...(value.tone === undefined ? {} : { tone: oneOf(value.tone, ['success', 'warning', 'danger'] as const, 'success', `${path}.tone`) }),
  }
}

/** The label, hint, error and flags every text field shares. */
function field(value: Record<string, unknown>, at: (field: string) => string) {
  return {
    name: name(value.name, at('name')),
    label: text(value.label, at('label'), TREE_LIMITS.label),
    hint: optText(value.hint, at('hint'), TREE_LIMITS.label),
    error: optText(value.error, at('error'), TREE_LIMITS.label),
    required: bool(value.required, false, at('required')),
    disabled: bool(value.disabled, false, at('disabled')),
  }
}

/** What a text field starts with: a number or boolean is written exactly as JavaScript spells it. */
function fieldValue(value: unknown, path: string): string {
  if (value === undefined || value === null) return ''
  if (typeof value === 'number') {
    if (!Number.isFinite(value)) fail(path, 'must be a finite number')
    return String(value)
  }
  if (typeof value === 'boolean') return String(value)
  return text(value, path, TREE_LIMITS.text, true)
}

/** `null` and `false` children are skipped, so a plugin can write `cond ? node : null`; paths keep the plugin's own indexes. */
function children(raw: unknown, path: string, depth: number, budget: Walk): UiNode[] {
  if (raw === undefined) return []
  const result: UiNode[] = []
  array(raw, path, TREE_LIMITS.nodes).forEach((child, index) => {
    if (child !== null && child !== false) result.push(node(child, `${path}[${index}]`, depth + 1, budget))
  })
  return result
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

function isNodeType(value: unknown): value is NodeType {
  return typeof value === 'string' && (NODE_TYPES as readonly string[]).includes(value)
}

/** A record of a fixed shape: `what` names it in the error (`a table column`). */
function fields(value: unknown, allowed: readonly string[], path: string, what: string): Record<string, unknown> {
  const result = record(value, path)
  only(result, allowed, path, what)
  return result
}

function only(value: Record<string, unknown>, allowed: readonly string[], path: string, what: string): void {
  for (const field of Object.keys(value)) {
    if (!allowed.includes(field)) fail(`${path}${member(field)}`, `is not a field of ${what}`)
  }
}

/** A field as a path segment: `.varient`, or quoted when it is not a plain name (`["on click"]`). */
function member(field: string): string {
  return /^[A-Za-z_$][\w$]{0,63}$/u.test(field) ? `.${field}` : `[${JSON.stringify([...field].slice(0, 64).join(''))}]`
}

function array(value: unknown, path: string, limit: number): unknown[] {
  if (!Array.isArray(value)) fail(path, 'must be a list')
  const list = value as unknown[]
  if (list.length > limit) fail(path, `has ${list.length} entries; at most ${limit}`)
  return list
}

/**
 * A string within `limit` characters. Characters are code points, so an emoji
 * counts once. UTF-16 length bounds the count from both sides (count ≤ length ≤
 * 2 × count), so only strings between `limit` and `2 × limit` units are counted.
 */
function text(value: unknown, path: string, limit: number, allowEmpty = false): string {
  if (typeof value !== 'string') fail(path, 'must be a string')
  const result = value as string
  if (!allowEmpty && result.length === 0) fail(path, 'must not be empty')
  if (result.length > limit && (result.length > limit * 2 || [...result].length > limit)) fail(path, `is longer than ${limit} characters`)
  return result
}

function optText(value: unknown, path: string, limit: number): string {
  return value === undefined || value === null ? '' : text(value, path, limit, true)
}

/** A metric or kv value: numbers stay numbers (a non-finite one becomes null), and the window formats them. */
function scalar(value: unknown, path: string): Scalar {
  if (value === null) return null
  if (typeof value === 'number') return Number.isFinite(value) ? value : null
  if (typeof value === 'boolean') return value
  return text(value, path, TREE_LIMITS.text, true)
}

function cell(value: unknown, path: string): CellValue {
  return value === undefined ? null : scalar(value, path)
}

function rowId(value: unknown, path: string): string | number {
  if (typeof value === 'number' && Number.isFinite(value)) return value
  if (typeof value !== 'string') fail(path, 'must be a string or a finite number')
  return text(value, path, TREE_LIMITS.text)
}

function finite(value: unknown, path: string): number | undefined {
  if (value === undefined) return undefined
  if (typeof value !== 'number' || !Number.isFinite(value)) fail(path, 'must be a finite number')
  return value as number
}

function number(value: unknown, path: string): number {
  return finite(needed(value, path), path) as number
}

/** A field with no default: absent is its own error, not "must be a …". */
function needed(value: unknown, path: string): unknown {
  if (value === undefined) fail(path, 'is missing')
  return value
}

/**
 * A slider value as the control holds it: inside the range and on a step
 * counted from `min`, the way a native range input snaps it. Rounding to 12
 * significant digits keeps `0.1 + 0.2` reading as `0.3`.
 */
export function onStep(value: number, min: number, max: number, step: number): number {
  const top = Math.floor((max - min) / step + 1e-9)
  const steps = Math.min(top, Math.max(0, Math.round((value - min) / step)))
  return Number((min + steps * step).toPrecision(12))
}

/** Image sources: inline pictures only, so a page can never make the window fetch anything. */
const IMAGE_SOURCE = /^data:image\/(?:png|jpeg|gif|webp);base64,[A-Za-z0-9+/]+={0,2}$/u
const HEX_COLOR = /^#[0-9a-f]{6}$/iu

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
