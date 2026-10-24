import { createContext, useContext, useEffect, useRef, useState, type CSSProperties, type KeyboardEvent, type ReactNode } from 'react'
import { ExternalLink } from 'lucide-react'
import { pluginIcon } from '../../../app/plugin-icons'
import { openExternal } from '../../../platform/lifecycle/open-external'
import { EmptyState, Metric, MetricStrip, Panel, Pill } from '../../../shared/ui/chrome'
import { Button } from '../../../shared/ui/Button'
import type { PageEvent } from '../domain/protocol'
import type { CellValue, Option, TableNode, UiNode } from '../domain/ui-tree'
import chrome from '../../../shared/ui/chrome.module.css'
import styles from './PluginTree.module.css'

type FieldValue = string | boolean
type Send = (event: Omit<PageEvent, 'form'>) => void

interface Form {
  readonly values: Map<string, FieldValue>
  readonly send: Send
  readonly disabled: boolean
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
export function PluginTree({ tree, disabled, onEvent }: {
  readonly tree: UiNode
  /** While the plugin is not running its controls cannot do anything. */
  readonly disabled: boolean
  readonly onEvent: (event: PageEvent) => void
}) {
  const values = useRef(new Map<string, FieldValue>())
  const handler = useRef(onEvent)
  useEffect(() => { handler.current = onEvent }, [onEvent])
  const [form] = useState<Omit<Form, 'disabled'>>(() => ({
    values: values.current,
    send: (event) => handler.current({ ...event, form: Object.fromEntries(values.current) }),
  }))
  return (
    <FormContext value={{ ...form, disabled }}>
      <div className={styles.tree}>
        <Node node={tree} />
      </div>
    </FormContext>
  )
}

function Nodes({ nodes }: { readonly nodes: readonly UiNode[] }) {
  return nodes.map((node, index) => <Node key={node.key ?? `${node.type}:${index}`} node={node} />)
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
      return <MetricStrip><Metric label={node.label} value={node.value} detail={node.detail || undefined} tone={node.tone} /></MetricStrip>
    case 'metrics':
      return (
        <MetricStrip>
          {node.items.map((item, index) => <Metric key={`${item.label}:${index}`} label={item.label} value={item.value} detail={item.detail || undefined} tone={item.tone} />)}
        </MetricStrip>
      )
    case 'badge':
      return <Pill tone={node.tone}>{node.text}</Pill>
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
              <dd>{item.value}</dd>
            </div>
          ))}
        </dl>
      )
    case 'button':
      return <PluginButton node={node} />
    case 'input':
      return <TextField node={node} />
    case 'select':
      return <SelectField node={node} />
    case 'segmented':
      return <SegmentedField node={node} />
    case 'toggle':
      return <ToggleField node={node} />
    case 'progress':
      return (
        <div className={styles.progress} data-tone={node.tone}>
          {node.label ? <span className={styles.progressLabel}>{node.label}<span>{Math.round(node.value * 100)}%</span></span> : null}
          <div
            className={styles.progressTrack}
            role="progressbar"
            aria-label={node.label || 'Progress'}
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={Math.round(node.value * 100)}
          >
            <span style={{ transform: `scaleX(${node.value})` }} />
          </div>
        </div>
      )
    case 'divider':
      return <hr className={styles.divider} />
    case 'empty':
      return <EmptyState icon={pluginIcon(undefined)} title={node.title} hint={node.hint || undefined} height={120} />
    case 'link':
      return (
        <button type="button" className={styles.link} onClick={() => void openExternal(node.href)} title={node.href}>
          {node.text}
          <ExternalLink size={12} aria-hidden="true" />
        </button>
      )
  }
}

function PluginButton({ node }: { readonly node: Extract<UiNode, { type: 'button' }> }) {
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

function Table({ node }: { readonly node: TableNode }) {
  const form = useForm()
  if (node.rows.length === 0) return <p className={styles.empty}>{node.empty}</p>
  const clickable = node.rowAction !== '' && !form.disabled
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
              tabIndex={clickable ? 0 : undefined}
              onClick={clickable ? () => activate(row, index) : undefined}
              onKeyDown={clickable ? (event: KeyboardEvent) => {
                if (event.key !== 'Enter' && event.key !== ' ') return
                event.preventDefault()
                activate(row, index)
              } : undefined}
            >
              {node.columns.map((column) => (
                <td key={column.key} data-align={column.align} data-mono={column.mono || undefined}>{cellText(row[column.key])}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function cellText(value: CellValue | undefined): string {
  if (value === null || value === undefined) return '—'
  if (typeof value === 'boolean') return value ? 'Yes' : 'No'
  if (typeof value === 'number') return value.toLocaleString()
  return value
}

/** The value the operator set, until the plugin sends a different one. */
function useFieldValue<T extends FieldValue>(name: string, fromTree: T): [T, (value: T) => void] {
  const form = useForm()
  const [draft, setDraft] = useState(fromTree)
  const [previous, setPrevious] = useState(fromTree)
  let current = draft
  if (previous !== fromTree) {
    setPrevious(fromTree)
    setDraft(fromTree)
    current = fromTree
  }
  const latest = useRef(current)
  latest.current = current
  form.values.set(name, current)
  useEffect(() => {
    form.values.set(name, latest.current)
    return () => { form.values.delete(name) }
  }, [form.values, name])
  return [current, (value: T) => {
    latest.current = value
    form.values.set(name, value)
    setDraft(value)
  }]
}

function TextField({ node }: { readonly node: Extract<UiNode, { type: 'input' }> }) {
  const form = useForm()
  const [value, setValue] = useFieldValue(node.name, node.value)
  const committed = useRef(node.value)
  useEffect(() => { committed.current = node.value }, [node.value])
  const commit = () => {
    if (committed.current === value) return
    committed.current = value
    form.send({ kind: 'change', name: node.name, value })
  }
  return (
    <label className={styles.field}>
      <span>{node.label}</span>
      <input
        type={node.kind === 'number' ? 'text' : node.kind}
        inputMode={node.kind === 'number' ? 'decimal' : undefined}
        value={value}
        placeholder={node.placeholder || undefined}
        maxLength={node.maxLength}
        disabled={node.disabled || form.disabled}
        onChange={(event) => setValue(event.target.value)}
        onBlur={commit}
        onKeyDown={(event) => {
          if (event.key !== 'Enter') return
          event.preventDefault()
          if (node.action) {
            committed.current = value
            form.send({ kind: 'action', action: node.action, name: node.name, value })
          } else {
            commit()
          }
        }}
      />
    </label>
  )
}

function SelectField({ node }: { readonly node: Extract<UiNode, { type: 'select' }> }) {
  const form = useForm()
  const [value, setValue] = useFieldValue(node.name, node.value)
  return (
    <label className={styles.field}>
      <span>{node.label}</span>
      <select
        value={value}
        disabled={node.disabled || form.disabled}
        onChange={(event) => {
          setValue(event.target.value)
          form.send({ kind: 'change', name: node.name, value: event.target.value })
        }}
      >
        {options(node.options, value).map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
      </select>
    </label>
  )
}

/** A value the plugin set that is not among its options still shows. */
function options(list: readonly Option[], value: string): readonly Option[] {
  return list.some((option) => option.value === value) ? list : [{ value, label: value || '—' }, ...list]
}

function SegmentedField({ node }: { readonly node: Extract<UiNode, { type: 'segmented' }> }) {
  const form = useForm()
  const [value, setValue] = useFieldValue(node.name, node.value)
  const disabled = node.disabled || form.disabled
  return (
    <div className={chrome.segmented} role="radiogroup" aria-label={node.label}>
      {node.options.map((option) => (
        <button
          key={option.value}
          type="button"
          role="radio"
          aria-checked={option.value === value}
          data-active={option.value === value || undefined}
          disabled={disabled}
          onClick={() => {
            if (option.value === value) return
            setValue(option.value)
            form.send({ kind: 'change', name: node.name, value: option.value })
          }}
        >
          {option.label}
        </button>
      ))}
    </div>
  )
}

function ToggleField({ node }: { readonly node: Extract<UiNode, { type: 'toggle' }> }) {
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
