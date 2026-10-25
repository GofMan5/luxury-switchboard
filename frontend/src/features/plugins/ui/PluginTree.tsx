import { createContext, useContext, useEffect, useId, useLayoutEffect, useRef, useState, type CSSProperties, type MouseEvent, type ReactNode } from 'react'
import { CircleAlert, CircleCheck, ExternalLink, Info, OctagonAlert, TriangleAlert, type LucideIcon } from 'lucide-react'
import { pluginIcon } from '../../../app/plugin-icons'
import { openExternal } from '../../../platform/lifecycle/open-external'
import { EmptyState, Metric, MetricStrip, Panel, Pill } from '../../../shared/ui/chrome'
import { Button } from '../../../shared/ui/Button'
import type { CanvasChannel } from '../domain/canvas'
import { isSafeHref, parseMarkdown, type Block, type Inline } from '../domain/markdown'
import type { PageEvent } from '../domain/protocol'
import { PluginCanvas } from './PluginCanvas'
import { formatNumber, formatScalar, type CellValue, type Option, type TableNode, type Tone, type UiNode } from '../domain/ui-tree'
import chrome from '../../../shared/ui/chrome.module.css'
import styles from './PluginTree.module.css'

type FieldValue = string | number | boolean
/** Every named control's current value, by name. Whoever sends page events reads it. */
export type FormValues = Map<string, FieldValue>
type Send = (event: Omit<PageEvent, 'form'>) => void
type NodeOf<T extends UiNode['type']> = Extract<UiNode, { type: T }>

interface Form {
  readonly values: FormValues
  /** Which mounted field wrote each name, so a field that unmounts only removes its own value. */
  readonly owners: Map<string, object>
  readonly send: Send
  readonly disabled: boolean
  /** Where canvas nodes get their draws and send their input; absent outside a running page. */
  readonly canvas?: { readonly channel: CanvasChannel; readonly surface: string }
}

const FormContext = createContext<Form | null>(null)

function useForm(): Form {
  const form = useContext(FormContext)
  if (!form) throw new Error('Plugin fields render inside a plugin tree')
  return form
}

/**
 * Draws a checked plugin tree with the Switchboard's own components. Every
 * event carries the current value of every field on the page, so a plugin
 * reads its form from the event instead of tracking keystrokes.
 */
export function PluginTree({ tree, disabled, onEvent, values: shared, canvas, surface }: {
  readonly tree: UiNode
  /** While the plugin is not running its controls cannot do anything. */
  readonly disabled: boolean
  readonly onEvent: (event: PageEvent) => void
  /** The page's form, when the caller sends events of its own (show/hide) with it. */
  readonly values?: FormValues
  /** The plugin host's canvas channel; without it and {@link surface} canvas nodes draw a placeholder. */
  readonly canvas?: CanvasChannel
  /** The page or surface id this tree was rendered for, as the channel names it. */
  readonly surface?: string
}) {
  const [own] = useState<FormValues>(() => new Map())
  const values = shared ?? own
  // Ownership belongs to one map: a new form starts with no owners.
  const [scope, setScope] = useState(() => ({ values, owners: new Map<string, object>() }))
  if (scope.values !== values) setScope({ values, owners: new Map() })
  const form: Form = {
    values: scope.values,
    owners: scope.owners,
    disabled,
    send: (event) => onEvent({ ...event, form: Object.fromEntries(scope.values) }),
    ...(canvas && surface ? { canvas: { channel: canvas, surface } } : {}),
  }
  return (
    <FormContext value={form}>
      <div className={styles.tree}>
        <Node node={tree} />
      </div>
    </FormContext>
  )
}

/** Plugin keys first, then field names, then position: a field keeps its state while siblings come and go. */
function Nodes({ nodes }: { readonly nodes: readonly UiNode[] }) {
  const used = new Set<string>()
  return nodes.map((node, index) => {
    let key = node.key !== undefined ? `key:${node.key}` : 'name' in node ? `name:${node.name}` : `${node.type}:${index}`
    while (used.has(key)) key = `${key}~${index}`
    used.add(key)
    return <Node key={key} node={node} />
  })
}

function Node({ node }: { readonly node: UiNode }): ReactNode {
  switch (node.type) {
    case 'stack':
      return <div className={styles.stack} data-gap={node.gap}><Nodes nodes={node.children} /></div>
    case 'row':
      return <div className={styles.row} data-gap={node.gap} data-align={node.align} data-wrap={node.wrap || undefined}><Nodes nodes={node.children} /></div>
    case 'grid':
      return <div className={styles.grid} data-gap={node.gap} data-columns={node.columns} style={{ '--plugin-columns': node.columns } as CSSProperties}><Nodes nodes={node.children} /></div>
    case 'panel':
      return (
        <Panel title={node.title || '\u00a0'} subtitle={node.subtitle || undefined} actions={node.actions.length ? <Nodes nodes={node.actions} /> : undefined}>
          {node.children.length ? <div className={styles.panelBody}><Nodes nodes={node.children} /></div> : null}
        </Panel>
      )
    case 'heading': {
      // h1 is the page title the host draws; plugin headings start below it.
      const Tag = node.level === 1 ? 'h2' : node.level === 2 ? 'h3' : 'h4'
      return <Tag className={styles.heading} data-level={node.level}>{node.text}</Tag>
    }
    case 'text':
      return <p className={styles.text} data-tone={node.tone} data-size={node.size} data-weight={node.weight} data-mono={node.mono || undefined}>{node.text}</p>
    case 'metric':
      return <MetricStrip><Metric label={node.label} value={formatScalar(node.value)} detail={node.detail || undefined} tone={node.tone} /></MetricStrip>
    case 'metrics':
      return (
        <MetricStrip>
          {node.items.map((item, index) => <Metric key={`${item.label}:${index}`} label={item.label} value={formatScalar(item.value)} detail={item.detail || undefined} tone={item.tone} />)}
        </MetricStrip>
      )
    case 'badge':
      return <span className={styles.badge}><Pill tone={node.tone}>{node.text}</Pill></span>
    case 'dot':
      return (
        <span className={styles.dot}>
          <span className="status-dot" data-tone={node.tone} aria-hidden="true" />
          {node.text ? <span>{node.text}</span> : null}
        </span>
      )
    case 'table':
      return <Table node={node} />
    case 'list':
      return node.items.length === 0 ? <p className={styles.empty}>{node.empty}</p> : (
        <ul className={styles.list}>
          {node.items.map((item, index) => (
            <li key={`${item.title}:${index}`}>
              {item.tone ? <span className="status-dot" data-tone={item.tone} aria-hidden="true" /> : null}
              <div>
                <strong>{item.title}</strong>
                {item.detail ? <p>{item.detail}</p> : null}
              </div>
              {item.meta ? <span className={styles.meta}>{item.meta}</span> : null}
            </li>
          ))}
        </ul>
      )
    case 'code':
      return <pre className={styles.code}><code>{node.text}</code></pre>
    case 'kv':
      return (
        <dl className={styles.kv}>
          {node.items.map((item, index) => (
            <div key={`${item.key}:${index}`}>
              <dt>{item.key}</dt>
              <dd>{formatScalar(item.value)}</dd>
            </div>
          ))}
        </dl>
      )
    case 'button':
      return <PluginButton node={node} />
    case 'input':
      return <TextField node={node} />
    case 'textarea':
      return <TextareaField node={node} />
    case 'select':
      return <SelectField node={node} />
    case 'segmented':
      return <SegmentedField node={node} />
    case 'toggle':
      return <ToggleField node={node} />
    case 'tabs':
      return <TabsField node={node} />
    case 'progress':
      return <Progress node={node} />
    case 'chart':
      return <Chart node={node} />
    case 'callout':
      return <Callout node={node} />
    case 'divider':
      return <hr className={styles.divider} />
    case 'empty':
      return <EmptyState icon={pluginIcon(undefined)} title={node.title} hint={node.hint || undefined} height={120} />
    case 'link':
      return <PluginLink node={node} />
    case 'markdown':
      return <div className={styles.markdown}><MarkdownBlocks blocks={parseMarkdown(node.text)} /></div>
    case 'image':
      // The checker admits only data:image URLs, so an image can never make the window fetch.
      return <img className={styles.image} src={node.src} alt={node.alt} width={node.width} height={node.height} />
    case 'slider':
      return <SliderField node={node} />
    case 'color':
      return <ColorField node={node} />
    case 'details':
      return <DetailsField node={node} />
    case 'canvas':
      return <CanvasField node={node} />
  }
}

/** Markdown drawn as React elements; the parser never produces HTML, so `<b>` stays text. */
function MarkdownBlocks({ blocks }: { readonly blocks: readonly Block[] }): ReactNode {
  return blocks.map((block, index) => {
    switch (block.kind) {
      case 'heading': {
        const Tag = block.level === 1 ? 'h2' : block.level === 2 ? 'h3' : 'h4'
        return <Tag key={index} className={styles.heading} data-level={block.level}><MarkdownInline nodes={block.children} /></Tag>
      }
      case 'paragraph': return <p key={index}><MarkdownInline nodes={block.children} /></p>
      case 'code': return <pre key={index} className={styles.code}><code>{block.text}</code></pre>
      case 'quote': return <blockquote key={index}><MarkdownBlocks blocks={block.children} /></blockquote>
      case 'rule': return <hr key={index} className={styles.divider} />
      case 'list': {
        const items = block.items.map((item, at) => <li key={at}><MarkdownBlocks blocks={item} /></li>)
        return block.ordered ? <ol key={index} start={block.start}>{items}</ol> : <ul key={index}>{items}</ul>
      }
    }
  })
}

function MarkdownInline({ nodes }: { readonly nodes: readonly Inline[] }): ReactNode {
  return nodes.map((node, index) => {
    switch (node.kind) {
      case 'text': return node.text
      case 'code': return <code key={index}>{node.text}</code>
      case 'strong': return <strong key={index}><MarkdownInline nodes={node.children} /></strong>
      case 'em': return <em key={index}><MarkdownInline nodes={node.children} /></em>
      case 'link':
        // The parser already refused unsafe targets; checked again because this is where the click happens.
        return isSafeHref(node.href)
          ? <ExternalAnchor key={index} href={node.href}><MarkdownInline nodes={node.children} /></ExternalAnchor>
          : <MarkdownInline key={index} nodes={node.children} />
    }
  })
}

/** A native range input: keyboard, focus and the spoken value come for free. Commits once the operator lets go. */
function SliderField({ node }: { readonly node: NodeOf<'slider'> }) {
  const form = useForm()
  const id = useId()
  const [value, setValue] = useFieldValue(node.name, node.value)
  const { commit } = useCommit(node.name, node.value, value)
  return (
    <div className={styles.field}>
      <label htmlFor={id} className={styles.sliderLabel}>
        <span>{node.label}</span>
        <output htmlFor={id}>{formatNumber(value)}</output>
      </label>
      <input
        id={id}
        type="range"
        className={styles.slider}
        min={node.min}
        max={node.max}
        step={node.step}
        value={value}
        disabled={node.disabled || form.disabled}
        onChange={(event) => setValue(Number(event.target.value))}
        onPointerUp={() => commit()}
        onKeyUp={() => commit()}
        onBlur={() => commit()}
      />
    </div>
  )
}

function ColorField({ node }: { readonly node: NodeOf<'color'> }) {
  const form = useForm()
  const id = useId()
  const [value, setValue] = useFieldValue(node.name, node.value)
  const { commit } = useCommit(node.name, node.value, value)
  return (
    <div className={styles.field}>
      <label htmlFor={id}>{node.label}</label>
      <span className={styles.color}>
        <input
          id={id}
          type="color"
          value={value}
          disabled={node.disabled || form.disabled}
          // React's onChange is the input event (every drag step); the native change fires once the picker closes.
          onChange={(event) => setValue(event.target.value.toLowerCase())}
          onBlur={() => commit()}
          ref={(input) => {
            if (!input) return
            const done = () => commit(input.value.toLowerCase())
            input.addEventListener('change', done)
            return () => input.removeEventListener('change', done)
          }}
        />
        <code aria-hidden="true">{value}</code>
      </span>
    </div>
  )
}

/** Native disclosure; the open state follows the plugin's tree until the operator toggles it. */
function DetailsField({ node }: { readonly node: NodeOf<'details'> }) {
  const [open, setOpen] = useState(node.open)
  const [previous, setPrevious] = useState(node.open)
  if (previous !== node.open) {
    setPrevious(node.open)
    setOpen(node.open)
  }
  return (
    <details className={styles.details} open={open} onToggle={(event) => setOpen(event.currentTarget.open)}>
      <summary>{node.summary}</summary>
      {open ? <div className={styles.detailsBody}><Nodes nodes={node.children} /></div> : null}
    </details>
  )
}

function CanvasField({ node }: { readonly node: NodeOf<'canvas'> }) {
  const form = useForm()
  return <PluginCanvas node={node} channel={form.canvas?.channel} surface={form.canvas?.surface} disabled={form.disabled} />
}

function PluginButton({ node }: { readonly node: NodeOf<'button'> }) {
  const form = useForm()
  const Icon = node.icon ? pluginIcon(node.icon) : null
  return (
    <Button
      type="button"
      variant={node.variant === 'default' ? 'secondary' : node.variant}
      disabled={node.disabled || form.disabled}
      onClick={() => form.send({ kind: 'action', action: node.action })}
    >
      {Icon ? <Icon size={14} strokeWidth={1.8} /> : null}
      {node.text}
    </Button>
  )
}

/** A real link, so it reads and focuses as one, that always opens in the system browser. */
function PluginLink({ node }: { readonly node: NodeOf<'link'> }) {
  return (
    <ExternalAnchor href={node.href}>
      {node.text}
      <ExternalLink size={12} aria-hidden="true" />
    </ExternalAnchor>
  )
}

function ExternalAnchor({ href, children }: { readonly href: string; readonly children: ReactNode }) {
  // Enter on a link is a click, so the keyboard takes the same path as the mouse.
  const open = (event: MouseEvent) => {
    event.preventDefault()
    void openExternal(href)
  }
  return (
    <a
      className={styles.link}
      href={href}
      title={href}
      rel="noreferrer"
      onClick={open}
      onAuxClick={(event) => { if (event.button === 1) open(event) }}
    >
      {children}
    </a>
  )
}

/** Cells longer than this show their full text on hover. */
const LONG_CELL = 40

function Table({ node }: { readonly node: TableNode }) {
  const form = useForm()
  if (node.rows.length === 0) return <p className={styles.empty}>{node.empty}</p>
  const actionable = node.rowAction !== ''
  const clickable = actionable && !form.disabled
  const activate = (row: Readonly<Record<string, CellValue>>, index: number) => {
    const id = row.id
    form.send({ kind: 'action', action: node.rowAction, row: typeof id === 'string' || typeof id === 'number' ? id : index })
  }
  return (
    <div className={styles.tableWrap}>
      <table className={styles.table}>
        <colgroup>{node.columns.map((column) => <col key={column.key} style={{ width: column.width === 'auto' ? undefined : column.width }} />)}</colgroup>
        <thead>
          <tr>{node.columns.map((column) => <th key={column.key} scope="col" data-align={column.align}>{column.label}</th>)}</tr>
        </thead>
        <tbody>
          {node.rows.map((row, index) => (
            <tr
              key={typeof row.id === 'string' || typeof row.id === 'number' ? `id:${row.id}` : index}
              data-clickable={clickable || undefined}
              // The whole row answers the mouse; the button in its first cell is the keyboard's way in.
              onClick={clickable ? (event) => { if (!(event.target as Element).closest('button')) activate(row, index) } : undefined}
            >
              {node.columns.map((column, at) => {
                const text = formatScalar(row[column.key], column.format)
                const title = [...text].length > LONG_CELL ? text : undefined
                return (
                  <td key={column.key} data-align={column.align} data-mono={column.mono || undefined} data-numeric={column.format !== 'text' || undefined} title={title}>
                    {actionable && at === 0 ? (
                      <button type="button" className={styles.rowButton} disabled={form.disabled} aria-label={text === '—' || text === '' ? `Row ${index + 1}` : text} onClick={() => activate(row, index)}>
                        {text}
                      </button>
                    ) : text}
                  </td>
                )
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

/**
 * The value the operator set, until the plugin sends a different one. The
 * form map is written after commit (and by the setter, inside event handlers),
 * never during render, so a render React throws away cannot leak into it.
 */
function useFieldValue<T extends FieldValue>(name: string, fromTree: T): [T, (value: T) => void] {
  const form = useForm()
  const [draft, setDraft] = useState(fromTree)
  const [previous, setPrevious] = useState(fromTree)
  const [owner] = useState(() => ({}))
  let current = draft
  if (previous !== fromTree) {
    setPrevious(fromTree)
    setDraft(fromTree)
    current = fromTree
  }
  const { values, owners } = form
  // Layout effects run before any handler or the page's own show event can read the map.
  useLayoutEffect(() => {
    owners.set(name, owner)
    values.set(name, current)
  }, [values, owners, owner, name, current])
  // Removal waits for the passive phase, after the page's hide event has read the form.
  useEffect(() => () => {
    if (owners.get(name) !== owner) return
    owners.delete(name)
    values.delete(name)
  }, [values, owners, owner, name])
  return [current, (value: T) => {
    values.set(name, value)
    setDraft(value)
  }]
}

/** The label, hint and error every text field draws, tied to the control for assistive technology. */
function Field({ id, node, error, children }: {
  readonly id: string
  readonly node: { readonly label: string; readonly hint: string; readonly required: boolean }
  readonly error: string
  readonly children: ReactNode
}) {
  return (
    <div className={styles.field} data-invalid={error ? true : undefined}>
      <label htmlFor={id}>
        {node.label}
        {node.required ? <span className={styles.required} aria-hidden="true">*</span> : null}
      </label>
      {children}
      {node.hint ? <small id={`${id}-hint`} className={styles.hint}>{node.hint}</small> : null}
      {error ? (
        <small id={`${id}-error`} className={styles.error}>
          <CircleAlert size={12} aria-hidden="true" />
          <span>{error}</span>
        </small>
      ) : null}
    </div>
  )
}

function describedBy(id: string, hint: string, error: string): string | undefined {
  return [hint ? `${id}-hint` : '', error ? `${id}-error` : ''].filter(Boolean).join(' ') || undefined
}

/** A number field's own range check; the plugin's error, when it sends one, takes its place. */
function rangeProblem(node: NodeOf<'input'>, value: string): string {
  if (node.kind !== 'number' || value.trim() === '') return ''
  const amount = Number(value)
  if (!Number.isFinite(amount)) return 'Enter a number.'
  const { min, max } = node
  if ((min !== undefined && amount < min) || (max !== undefined && amount > max)) {
    if (min !== undefined && max !== undefined) return `Enter a number from ${formatNumber(min)} to ${formatNumber(max)}.`
    return min !== undefined ? `Enter ${formatNumber(min)} or more.` : `Enter ${formatNumber(max ?? 0)} or less.`
  }
  return ''
}

/** Commits a text value as a change once, when it differs from what the plugin last saw. */
function useCommit<T extends FieldValue>(name: string, fromTree: T, value: T) {
  const form = useForm()
  const committed = useRef(fromTree)
  useEffect(() => { committed.current = fromTree }, [fromTree])
  return {
    commit: (next: T = value) => {
      if (committed.current === next) return
      committed.current = next
      form.send({ kind: 'change', name, value: next })
    },
    act: (action: string) => {
      committed.current = value
      form.send({ kind: 'action', action, name, value })
    },
  }
}

function TextField({ node }: { readonly node: NodeOf<'input'> }) {
  const form = useForm()
  const id = useId()
  const [value, setValue] = useFieldValue(node.name, node.value)
  const { commit, act } = useCommit(node.name, node.value, value)
  const error = node.error || rangeProblem(node, value)
  const number = node.kind === 'number'
  return (
    <Field id={id} node={node} error={error}>
      <input
        id={id}
        // A native number input empties a half-typed value ("1e", "-"), so numbers are text the field checks itself.
        type={number ? 'text' : node.kind}
        inputMode={number ? 'decimal' : undefined}
        value={value}
        placeholder={node.placeholder || undefined}
        maxLength={node.maxLength}
        required={node.required || undefined}
        aria-required={node.required || undefined}
        aria-invalid={error ? true : undefined}
        aria-describedby={describedBy(id, node.hint, error)}
        disabled={node.disabled || form.disabled}
        onChange={(event) => setValue(event.target.value)}
        onBlur={() => commit()}
        onKeyDown={(event) => {
          if (number && (event.key === 'ArrowUp' || event.key === 'ArrowDown')) {
            event.preventDefault()
            setValue(stepped(node, value, event.key === 'ArrowUp' ? 1 : -1))
            return
          }
          if (event.key !== 'Enter') return
          event.preventDefault()
          if (node.action) act(node.action)
          else commit()
        }}
      />
    </Field>
  )
}

/** ArrowUp/ArrowDown in a number field: one step, kept inside the range. */
function stepped(node: NodeOf<'input'>, value: string, direction: 1 | -1): string {
  const step = node.step ?? 1
  const current = value.trim() === '' || !Number.isFinite(Number(value)) ? (node.min ?? 0) : Number(value)
  let next = current + direction * step
  if (node.min !== undefined) next = Math.max(node.min, next)
  if (node.max !== undefined) next = Math.min(node.max, next)
  // Strip float noise (0.1 + 0.2) without changing a value the step can reach.
  return String(Number.parseFloat(next.toPrecision(12)))
}

function TextareaField({ node }: { readonly node: NodeOf<'textarea'> }) {
  const form = useForm()
  const id = useId()
  const [value, setValue] = useFieldValue(node.name, node.value)
  const { commit, act } = useCommit(node.name, node.value, value)
  return (
    <Field id={id} node={node} error={node.error}>
      <textarea
        id={id}
        value={value}
        rows={node.rows}
        placeholder={node.placeholder || undefined}
        maxLength={node.maxLength}
        required={node.required || undefined}
        aria-required={node.required || undefined}
        aria-invalid={node.error ? true : undefined}
        aria-describedby={describedBy(id, node.hint, node.error)}
        aria-keyshortcuts="Control+Enter"
        disabled={node.disabled || form.disabled}
        onChange={(event) => setValue(event.target.value)}
        onBlur={() => commit()}
        onKeyDown={(event) => {
          // Enter alone is a new line; Ctrl+Enter (⌘+Enter on a Mac) submits.
          if (event.key !== 'Enter' || !(event.ctrlKey || event.metaKey)) return
          event.preventDefault()
          if (node.action) act(node.action)
          else commit()
        }}
      />
    </Field>
  )
}

function SelectField({ node }: { readonly node: NodeOf<'select'> }) {
  const form = useForm()
  const id = useId()
  const [value, setValue] = useFieldValue(node.name, node.value)
  return (
    <div className={styles.field}>
      <label htmlFor={id}>{node.label}</label>
      <select
        id={id}
        value={value}
        disabled={node.disabled || form.disabled}
        onChange={(event) => {
          setValue(event.target.value)
          form.send({ kind: 'change', name: node.name, value: event.target.value })
        }}
      >
        {options(node.options, value).map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
      </select>
    </div>
  )
}

/** A value the plugin set that is not among its options still shows. */
function options(list: readonly Option[], value: string): readonly Option[] {
  return list.some((option) => option.value === value) ? list : [{ value, label: value || '—' }, ...list]
}

/** Where an arrow, Home or End key moves in a row of choices, or null for any other key. */
function moveIndex(key: string, index: number, count: number, vertical: boolean): number | null {
  switch (key) {
    case 'ArrowRight': return (index + 1) % count
    case 'ArrowLeft': return (index - 1 + count) % count
    case 'ArrowDown': return vertical ? (index + 1) % count : null
    case 'ArrowUp': return vertical ? (index - 1 + count) % count : null
    case 'Home': return 0
    case 'End': return count - 1
    default: return null
  }
}

/** A radio group: one tab stop, arrows move and choose, as everywhere else on the desktop. */
function SegmentedField({ node }: { readonly node: NodeOf<'segmented'> }) {
  const form = useForm()
  const [value, setValue] = useFieldValue(node.name, node.value)
  const buttons = useRef<(HTMLButtonElement | null)[]>([])
  const disabled = node.disabled || form.disabled
  const checked = node.options.findIndex((option) => option.value === value)
  const choose = (option: Option) => {
    if (option.value === value) return
    setValue(option.value)
    form.send({ kind: 'change', name: node.name, value: option.value })
  }
  return (
    <div className={chrome.segmented} role="radiogroup" aria-label={node.label} aria-disabled={disabled || undefined}>
      {node.options.map((option, index) => (
        <button
          key={option.value}
          ref={(element) => { buttons.current[index] = element }}
          type="button"
          role="radio"
          aria-checked={option.value === value}
          data-active={option.value === value || undefined}
          tabIndex={index === Math.max(checked, 0) ? 0 : -1}
          disabled={disabled}
          onClick={() => choose(option)}
          onKeyDown={(event) => {
            const next = moveIndex(event.key, index, node.options.length, true)
            if (next === null) return
            event.preventDefault()
            choose(node.options[next])
            buttons.current[next]?.focus()
          }}
        >
          {option.label}
        </button>
      ))}
    </div>
  )
}

function ToggleField({ node }: { readonly node: NodeOf<'toggle'> }) {
  const form = useForm()
  const [value, setValue] = useFieldValue(node.name, node.value)
  return (
    <label className={styles.toggle}>
      <button
        type="button"
        role="switch"
        className={styles.switch}
        aria-checked={value}
        disabled={node.disabled || form.disabled}
        onClick={() => {
          setValue(!value)
          form.send({ kind: 'change', name: node.name, value: !value })
        }}
      >
        <span className={styles.knob} aria-hidden="true" />
      </button>
      <span>{node.label}</span>
    </label>
  )
}

/**
 * Tabs draw only the chosen tab's children; the choice is a form value, so the
 * plugin hears it and can render that tab's content lazily. Switching tabs is
 * navigation, so it still works while the plugin is not running.
 */
function TabsField({ node }: { readonly node: NodeOf<'tabs'> }) {
  const form = useForm()
  const base = useId()
  const [value, setValue] = useFieldValue(node.name, node.value)
  const buttons = useRef<(HTMLButtonElement | null)[]>([])
  const found = node.tabs.findIndex((tab) => tab.id === value)
  const selected = Math.max(0, found)
  const tab = node.tabs[selected]
  const panel = `${base}-panel`
  // The operator's tab left the plugin's new tree: fall back to the plugin's choice, in the form too.
  useLayoutEffect(() => { if (found === -1) setValue(node.value) })
  const choose = (index: number) => {
    const id = node.tabs[index].id
    if (id === tab.id) return
    setValue(id)
    if (!form.disabled) form.send({ kind: 'change', name: node.name, value: id })
  }
  return (
    <div className={styles.tabs}>
      <div className={styles.tabList} role="tablist" aria-label={node.label || undefined}>
        {node.tabs.map((entry, index) => (
          <button
            key={entry.id}
            ref={(element) => { buttons.current[index] = element }}
            id={`${base}-tab-${index}`}
            type="button"
            role="tab"
            className={styles.tab}
            aria-selected={index === selected}
            aria-controls={panel}
            tabIndex={index === selected ? 0 : -1}
            onClick={() => choose(index)}
            onKeyDown={(event) => {
              const next = moveIndex(event.key, index, node.tabs.length, false)
              if (next === null) return
              event.preventDefault()
              choose(next)
              buttons.current[next]?.focus()
            }}
          >
            {entry.label}
          </button>
        ))}
      </div>
      <div className={styles.tabPanel} role="tabpanel" id={panel} aria-labelledby={`${base}-tab-${selected}`} tabIndex={0}>
        <Nodes nodes={tab.children} />
      </div>
    </div>
  )
}

function Progress({ node }: { readonly node: NodeOf<'progress'> }) {
  const percent = Math.round(node.value * 100)
  return (
    <div className={styles.progress} data-tone={node.tone}>
      {node.label ? <span className={styles.progressLabel}>{node.label}<span>{node.indeterminate ? 'Working…' : `${percent}%`}</span></span> : null}
      <div
        className={styles.progressTrack}
        role="progressbar"
        aria-label={node.label || 'Progress'}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={node.indeterminate ? undefined : percent}
        aria-busy={node.indeterminate || undefined}
        data-indeterminate={node.indeterminate || undefined}
      >
        <span style={node.indeterminate ? undefined : { transform: `scaleX(${node.value})` }} />
      </div>
    </div>
  )
}

/**
 * A small line or bar chart drawn in the tone's colour. It is one image to a
 * screen reader, named by its label and summarised by its range and last value.
 */
function Chart({ node }: { readonly node: NodeOf<'chart'> }) {
  const summary = useId()
  const { values, labels } = node
  const count = values.length
  const lowest = Math.min(...values)
  const highest = Math.max(...values)
  const last = values[count - 1]
  // Bars grow from zero when zero is in view; a line uses the data's own range.
  let lo = node.min ?? (node.kind === 'bars' ? Math.min(0, lowest) : lowest)
  let hi = node.max ?? (node.kind === 'bars' ? Math.max(0, highest) : highest)
  if (hi <= lo) { lo -= 1; hi += 1 }
  const y = (value: number) => 96 - ((Math.min(hi, Math.max(lo, value)) - lo) / (hi - lo)) * 92
  const describe = (index: number) => `${labels[index] ? `${labels[index]}: ` : ''}${formatNumber(values[index])}`
  return (
    <figure className={styles.chart} data-tone={node.tone}>
      <figcaption className={styles.chartHead}>
        <span>{node.label}</span>
        <span className={styles.chartLast} aria-hidden="true">{formatNumber(last)}</span>
      </figcaption>
      <svg
        className={styles.chartPlot}
        role="img"
        aria-label={node.label}
        aria-describedby={summary}
        viewBox="0 0 100 100"
        preserveAspectRatio="none"
        style={{ height: node.height }}
      >
        {lo < 0 && hi > 0 ? <line className={styles.chartBase} x1={0} x2={100} y1={y(0)} y2={y(0)} vectorEffect="non-scaling-stroke" /> : null}
        {node.kind === 'bars' ? values.map((value, index) => {
          const slot = 100 / count
          const top = Math.min(y(value), y(0))
          return (
            <rect key={index} x={index * slot + slot * 0.15} width={slot * 0.7} y={top} height={Math.max(0.5, Math.abs(y(value) - y(0)))} fill="currentColor">
              <title>{describe(index)}</title>
            </rect>
          )
        }) : (
          <polyline
            fill="none"
            stroke="currentColor"
            strokeWidth={1.75}
            strokeLinejoin="round"
            strokeLinecap="round"
            vectorEffect="non-scaling-stroke"
            points={values.map((value, index) => `${count === 1 ? 50 : (index / (count - 1)) * 100},${y(value)}`).join(' ')}
          />
        )}
      </svg>
      {labels.length ? (
        <div className={styles.chartAxis} aria-hidden="true">
          <span>{labels[0]}</span>
          {count > 1 ? <span>{labels[count - 1]}</span> : null}
        </div>
      ) : null}
      <p id={summary} className="sr-only">
        {`${count} ${count === 1 ? 'value' : 'values'}; lowest ${formatNumber(lowest)}, highest ${formatNumber(highest)}, last ${describe(count - 1)}.`}
      </p>
    </figure>
  )
}

const CALLOUT_ICONS: Readonly<Record<Tone, LucideIcon>> = {
  neutral: Info, info: Info, success: CircleCheck, warning: TriangleAlert, danger: OctagonAlert,
}

function Callout({ node }: { readonly node: NodeOf<'callout'> }) {
  const Icon = CALLOUT_ICONS[node.tone]
  return (
    <div className={styles.callout} role="note" data-tone={node.tone}>
      <Icon className={styles.calloutIcon} size={16} strokeWidth={1.8} aria-hidden="true" />
      <div className={styles.calloutBody}>
        {node.title ? <strong>{node.title}</strong> : null}
        <p>{node.text}</p>
        {node.actions.length ? <div className={styles.calloutActions}><Nodes nodes={node.actions} /></div> : null}
      </div>
    </div>
  )
}
