import { useMemo, useState, type KeyboardEvent } from 'react'
import { Plus, RotateCcw, Search } from 'lucide-react'
import { COLOR_TOKENS, DERIVED_TOKENS, TOKEN_LIMIT, tokenProblem, type TokenGroup, type TokenSpec } from '../../../shared/contracts/tokens'
import styles from './AppearancePage.module.css'

const HEX = /^#[0-9a-f]{6}$/iu
const GROUP_ORDER = ['Surface', 'Border', 'Text', 'Accent', 'Status', 'Layout', 'Effect', 'Motion'] as const satisfies readonly TokenGroup[]

/** Every public token as one dense, filterable table: the theme's value as
 * the placeholder, the override in the field. A row commits on Enter or blur,
 * so a half-typed value never reaches the window. */
export function TokenEditor({ overrides, resolved, onChange }: {
  readonly overrides: Readonly<Record<string, string>>
  readonly resolved: Readonly<Record<string, string>>
  readonly onChange: (tokens: Record<string, string>) => void
}) {
  const [filter, setFilter] = useState('')
  const [scope, setScope] = useState<'palette' | 'all' | 'changed'>('palette')
  const [custom, setCustom] = useState('')
  const [customError, setCustomError] = useState('')
  const count = Object.keys(overrides).length

  const rows = useMemo(() => {
    const catalog: TokenSpec[] = [...COLOR_TOKENS, ...(scope === 'palette' ? [] : DERIVED_TOKENS)]
    const known = new Set(catalog.map((spec) => spec.name))
    // Overrides outside the catalog (a plugin's own token, say) still list.
    for (const name of Object.keys(overrides)) {
      if (!known.has(name)) catalog.push({ name, group: 'Effect', description: 'Custom property' })
    }
    const needle = filter.trim().toLowerCase()
    return catalog.filter((spec) => (scope !== 'changed' || spec.name in overrides)
      && (!needle || spec.name.includes(needle) || spec.description.toLowerCase().includes(needle)))
  }, [filter, scope, overrides])

  const commit = (name: string, value: string) => {
    const next = { ...overrides }
    if (value.trim()) next[name] = value.trim()
    else delete next[name]
    onChange(next)
  }

  const addCustom = () => {
    const name = custom.trim()
    const problem = tokenProblem(name, 'initial')
    if (problem) { setCustomError(problem); return }
    if (count >= TOKEN_LIMIT && !(name in overrides)) { setCustomError(`At most ${TOKEN_LIMIT} overrides`); return }
    setCustomError('')
    setCustom('')
    setScope('changed')
    commit(name, overrides[name] ?? resolved[name] ?? 'initial')
  }

  return (
    <div className={styles.tokens}>
      <div className={styles.tokenToolbar}>
        <label className={styles.tokenSearch}>
          <Search size={13} aria-hidden="true" />
          <span className="sr-only">Filter tokens</span>
          <input type="search" value={filter} placeholder="Filter by name or role" onChange={(event) => setFilter(event.target.value)} spellCheck={false} />
        </label>
        <div className={styles.scope} role="group" aria-label="Tokens shown">
          {(['palette', 'all', 'changed'] as const).map((id) => (
            <button key={id} type="button" aria-pressed={scope === id} data-active={scope === id || undefined} onClick={() => setScope(id)}>
              {id === 'palette' ? 'Palette' : id === 'all' ? 'All' : `Changed ${count}`}
            </button>
          ))}
        </div>
        {count > 0 ? <button type="button" className={styles.linkButton} onClick={() => onChange({})}>Clear overrides</button> : null}
      </div>
      <div className={styles.tokenTable} role="table" aria-label="Design tokens">
        {GROUP_ORDER.map((group) => {
          const groupRows = rows.filter((row) => row.group === group)
          if (groupRows.length === 0) return null
          return (
            <div key={group} role="rowgroup">
              <div className={styles.tokenGroup} role="row"><span role="columnheader">{group}</span></div>
              {groupRows.map((spec) => (
                <TokenRow key={spec.name} spec={spec} override={overrides[spec.name] ?? ''} resolved={resolved[spec.name] ?? ''} onCommit={(value) => commit(spec.name, value)} />
              ))}
            </div>
          )
        })}
        {rows.length === 0 ? <p className={styles.tokenNone}>{scope === 'changed' ? 'No overrides yet — every token follows the theme.' : 'No token matches the filter.'}</p> : null}
      </div>
      <div className={styles.tokenAdd}>
        <label className={styles.tokenAddField}>
          <span className="sr-only">Custom property name</span>
          <input value={custom} placeholder="--my-plugin-token" spellCheck={false} aria-invalid={Boolean(customError) || undefined} onChange={(event) => { setCustom(event.target.value); setCustomError('') }} onKeyDown={(event) => { if (event.key === 'Enter') { event.preventDefault(); addCustom() } }} />
        </label>
        <button type="button" className="button button-ghost" onClick={addCustom} disabled={!custom.trim()}><Plus size={13} aria-hidden="true" />Add property</button>
        {customError ? <span className={styles.fieldError} role="alert">{customError}</span> : <span className={styles.tokenCount}>{count} / {TOKEN_LIMIT}</span>}
      </div>
    </div>
  )
}

function TokenRow({ spec, override, resolved, onCommit }: {
  readonly spec: TokenSpec
  readonly override: string
  readonly resolved: string
  readonly onCommit: (value: string) => void
}) {
  const [draft, setDraft] = useState<string | null>(null)
  const value = draft ?? override
  const problem = value.trim() ? tokenProblem(spec.name, value.trim()) : ''
  const effective = override || resolved
  const isColor = HEX.test(effective)
  const finish = () => {
    if (draft === null) return
    if (!problem) onCommit(draft)
    setDraft(null)
  }
  const keydown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'Enter') { event.preventDefault(); finish() }
    else if (event.key === 'Escape' && draft !== null) { event.preventDefault(); event.stopPropagation(); setDraft(null) }
  }
  return (
    <div className={styles.tokenRow} role="row" data-overridden={override ? true : undefined}>
      <span role="cell" className={styles.tokenSwatchCell}>
        {isColor ? (
          <label className={styles.tokenSwatch} style={{ background: effective }} title={`Pick ${spec.name}`}>
            <span className="sr-only">Pick a colour for {spec.name}</span>
            <input type="color" value={effective.toLowerCase()} onChange={(event) => onCommit(event.target.value)} />
          </label>
        ) : <span className={styles.tokenSwatchEmpty} aria-hidden="true" />}
      </span>
      <span role="cell" className={styles.tokenName} title={spec.description}>
        <code>{spec.name}</code>
        <span>{spec.description}</span>
      </span>
      <span role="cell" className={styles.tokenValue}>
        <input
          value={value}
          placeholder={resolved}
          aria-label={`${spec.name} value`}
          aria-invalid={Boolean(problem) || undefined}
          title={problem || undefined}
          spellCheck={false}
          onChange={(event) => setDraft(event.target.value)}
          onBlur={finish}
          onKeyDown={keydown}
        />
      </span>
      <span role="cell" className={styles.tokenReset}>
        <button type="button" aria-label={`Reset ${spec.name}`} title="Back to the theme value" disabled={!override} onClick={() => { setDraft(null); onCommit('') }}>
          <RotateCcw size={12} aria-hidden="true" />
        </button>
      </span>
    </div>
  )
}
