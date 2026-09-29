import { useMemo, useState } from 'react'
import { Coins, Trash2 } from 'lucide-react'
import type { ModelPrice } from '../domain/insights'
import type { PriceDraft } from '../application/insights-port'

interface PriceEditorProps {
  readonly prices: readonly ModelPrice[]
  readonly phase: 'idle' | 'loading' | 'saving' | 'ready' | 'error'
  readonly knownModels: readonly string[]
  readonly onSave: (draft: PriceDraft) => Promise<void>
  readonly onRemove: (model: string) => Promise<void>
}

type EditorState =
  | { readonly kind: 'closed' }
  | { readonly kind: 'open'; readonly model: string; readonly input: string; readonly cachedInput: string; readonly output: string; readonly reasoning: string }

const emptyDraft: EditorState = {
  kind: 'open', model: '', input: '', cachedInput: '', output: '', reasoning: '',
}

export function PriceEditor({ prices, phase, knownModels, onSave, onRemove }: PriceEditorProps) {
  const [state, setState] = useState<EditorState>({ kind: 'closed' })
  const knownList = useMemo(
    () => [...new Set([...knownModels, ...prices.map((price) => price.model)])],
    [knownModels, prices],
  )
  const existing = state.kind === 'open' ? prices.find((price) => price.model === state.model) : undefined
  const busy = phase === 'saving' || phase === 'loading'

  const open = (draft: EditorState) => setState(draft)
  const close = () => setState({ kind: 'closed' })
  const submit = () => {
    if (state.kind !== 'open') return
    const draft = normalizeDraft(state, existing)
    if (!draft) return
    void onSave(draft).then(close)
  }

  return (
    <>
      <button type="button" className="price-button" onClick={() => open(emptyDraft)} disabled={busy}>
        <Coins size={15} aria-hidden="true" />Prices
      </button>
      {state.kind === 'open' ? (
        <div className="modal-backdrop" role="presentation" onClick={close}>
          <section
            className="price-modal"
            role="dialog"
            aria-modal="true"
            aria-labelledby="price-title"
            onClick={(event) => event.stopPropagation()}
          >
            <header>
              <h2 id="price-title">Price catalog</h2>
              <p>Rates per one million tokens, in dollars. A zero field means that token kind is not priced, and models without a price are excluded from the estimate instead of guessed.</p>
            </header>
            <label className="price-field">
              <span>Model</span>
              <input
                list="price-models"
                value={state.model}
                placeholder="gpt-6-astra"
                autoFocus
                onChange={(event) => setState({ ...state, model: event.target.value })}
              />
              <datalist id="price-models">
                {knownList.map((model) => <option key={model} value={model} />)}
              </datalist>
            </label>
            <div className="price-grid">
              <NumberField label="Input" value={state.input} placeholder={placeholder(existing?.input)} onChange={(input) => setState({ ...state, input })} />
              <NumberField label="Cached input" value={state.cachedInput} placeholder={placeholder(existing?.cachedInput)} onChange={(cachedInput) => setState({ ...state, cachedInput })} />
              <NumberField label="Output" value={state.output} placeholder={placeholder(existing?.output)} onChange={(output) => setState({ ...state, output })} />
              <NumberField label="Reasoning" value={state.reasoning} hint="Defaults to the output rate when zero" placeholder={placeholder(existing?.reasoning)} onChange={(reasoning) => setState({ ...state, reasoning })} />
            </div>
            {phase === 'error' ? <p className="price-error" role="alert">The price was not saved. Check the rates and try again.</p> : null}
            <div className="price-actions">
              {existing ? (
                <button
                  type="button"
                  className="price-remove"
                  disabled={busy}
                  onClick={() => { void onRemove(existing.model).then(close) }}
                >
                  <Trash2 size={14} aria-hidden="true" />Remove
                </button>
              ) : null}
              <span className="price-spacer" />
              <button type="button" onClick={close} disabled={busy}>Cancel</button>
              <button type="button" className="price-save" onClick={submit} disabled={busy || state.model.trim() === ''}>
                {busy ? 'Saving…' : 'Save price'}
              </button>
            </div>
            {prices.length > 0 ? (
              <footer className="price-list">
                <h3>Saved rates</h3>
                {prices.map((price) => (
                  <button key={price.model} type="button" className="price-entry" onClick={() => setState({
                    kind: 'open', model: price.model,
                    input: String(price.input || ''), cachedInput: String(price.cachedInput || ''),
                    output: String(price.output || ''), reasoning: String(price.reasoning || ''),
                  })}>
                    <span>{price.model}</span>
                    <small>in {price.input || '—'} · out {price.output || '—'}</small>
                  </button>
                ))}
              </footer>
            ) : null}
          </section>
        </div>
      ) : null}
    </>
  )
}

function NumberField({ label, value, placeholder, hint, onChange }: {
  label: string
  value: string
  placeholder: string
  hint?: string
  onChange: (value: string) => void
}) {
  return (
    <label className="price-field">
      <span>{label}</span>
      <input
        type="number"
        min={0}
        step="any"
        value={value}
        placeholder={placeholder}
        onChange={(event) => onChange(event.target.value)}
      />
      {hint ? <small>{hint}</small> : null}
    </label>
  )
}

function placeholder(value: number | undefined): string {
  return value ? String(value) : '0'
}

function normalizeDraft(state: Extract<EditorState, { kind: 'open' }>, existing: ModelPrice | undefined): PriceDraft | null {
  const model = state.model.trim()
  if (!model) return null
  const parse = (raw: string, fallback: number): number => {
    const parsed = Number(raw)
    return raw.trim() !== '' && Number.isFinite(parsed) && parsed >= 0 ? parsed : fallback
  }
  return {
    model,
    input: parse(state.input, existing?.input ?? 0),
    cachedInput: parse(state.cachedInput, existing?.cachedInput ?? 0),
    output: parse(state.output, existing?.output ?? 0),
    reasoning: parse(state.reasoning, existing?.reasoning ?? 0),
  }
}
