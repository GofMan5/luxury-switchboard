import { useMemo, useState } from 'react'
import { Coins, Trash2 } from 'lucide-react'
import type { ModelPrice } from '../domain/insights'
import type { PriceDraft } from '../application/insights-port'
import { useModalFocus } from '../../../shared/ui/useModalFocus'

interface PriceEditorProps {
  readonly prices: readonly ModelPrice[]
  readonly currency: string
  readonly phase: 'idle' | 'loading' | 'saving' | 'ready' | 'error'
  readonly knownModels: readonly string[]
  readonly onSave: (draft: PriceDraft) => Promise<boolean>
  readonly onRemove: (model: string) => Promise<boolean>
  readonly onSetCurrency: (currency: string) => Promise<boolean>
}

type EditorState = {
  readonly model: string
  readonly input: string
  readonly cachedInput: string
  readonly output: string
  readonly reasoning: string
}

const emptyDraft: EditorState = { model: '', input: '', cachedInput: '', output: '', reasoning: '' }

/** The currencies most operators bill in; the field also takes any ISO code. */
const POPULAR_CURRENCIES = ['USD', 'EUR', 'CNY', 'RUB', 'GBP', 'JPY', 'KRW', 'INR', 'TRY', 'BRL', 'PLN', 'UAH', 'KZT', 'ILS', 'THB', 'VND', 'HKD', 'SGD', 'TWD', 'AED'] as const

export function PriceEditor({ prices, currency, phase, knownModels, onSave, onRemove, onSetCurrency }: PriceEditorProps) {
  const [open, setOpen] = useState(false)
  const busy = phase === 'saving' || phase === 'loading'
  const knownList = useMemo(
    () => [...new Set([...knownModels, ...prices.map((price) => price.model)])],
    [knownModels, prices],
  )
  return (
    <>
      <button type="button" className="price-button" onClick={() => setOpen(true)} disabled={busy}>
        <Coins size={15} aria-hidden="true" />Prices
      </button>
      {/* The dialog is its own component so the shared focus hook attaches on
          the mount it belongs to: a conditional child of a persistent parent
          would run the hook's effect while the dialog does not exist. */}
      {open ? (
        <PriceDialog
          prices={prices}
          currency={currency}
          phase={phase}
          knownList={knownList}
          onClose={() => setOpen(false)}
          onSave={onSave}
          onRemove={onRemove}
          onSetCurrency={onSetCurrency}
        />
      ) : null}
    </>
  )
}

interface PriceDialogProps {
  readonly prices: readonly ModelPrice[]
  readonly currency: string
  readonly phase: 'idle' | 'loading' | 'saving' | 'ready' | 'error'
  readonly knownList: readonly string[]
  readonly onClose: () => void
  readonly onSave: (draft: PriceDraft) => Promise<boolean>
  readonly onRemove: (model: string) => Promise<boolean>
  readonly onSetCurrency: (currency: string) => Promise<boolean>
}

function PriceDialog({ prices, currency, phase, knownList, onClose, onSave, onRemove, onSetCurrency }: PriceDialogProps) {
  const [state, setState] = useState<EditorState>(emptyDraft)
  const [currencyDraft, setCurrencyDraft] = useState(currency)
  const busy = phase === 'saving' || phase === 'loading'
  // Escape and the Tab trap behave like every other modal in the app: the
  // shared hook, not a per-modal reinvention.
  const dialogRef = useModalFocus<HTMLDivElement>(onClose, busy)
  const existing = prices.find((price) => price.model === state.model)
  const currencyCode = currencyDraft.trim().toUpperCase()
  const currencyValid = /^[A-Z]{3}$/.test(currencyCode)
  const currencyDirty = currencyValid && currencyCode !== currency

  // The dialog closes only on a confirmed save: a failed one keeps the
  // operator's typed rates on screen next to the error.
  async function submit() {
    const draft = normalizeDraft(state, existing)
    if (!draft) return
    if (await onSave(draft)) onClose()
  }

  return (
    <div className="modal-backdrop" role="presentation" onClick={onClose}>
      <section
        ref={dialogRef}
        className="price-modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="price-title"
        onClick={(event) => event.stopPropagation()}
      >
        <header>
          <h2 id="price-title">Price catalog</h2>
          <p>Rates per one million tokens, in {currency}. A zero field means that token kind is not priced, and models without a price are excluded from the estimate instead of guessed.</p>
          <div className="price-currency">
            <label className="price-field">
              <span>Currency</span>
              <input
                list="price-currencies"
                value={currencyDraft}
                maxLength={6}
                aria-label="Currency"
                placeholder="USD"
                disabled={busy}
                onChange={(event) => setCurrencyDraft(event.target.value.toUpperCase())}
              />
              <datalist id="price-currencies">
                {POPULAR_CURRENCIES.map((code) => <option key={code} value={code} />)}
              </datalist>
            </label>
            <button
              type="button"
              disabled={busy || !currencyDirty}
              onClick={() => void onSetCurrency(currencyCode)}
            >
              Apply
            </button>
          </div>
          <p className="price-currency-note">The catalog converts nothing: rates stay the numbers you entered, only the unit they read in changes.</p>
        </header>
        <label className="price-field">
          <span>Model</span>
          <input
            list="price-models"
            value={state.model}
            placeholder="gpt-6-astra"
            maxLength={128}
            data-autofocus
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
              onClick={async () => { if (await onRemove(existing.model)) onClose() }}
            >
              <Trash2 size={14} aria-hidden="true" />Remove
            </button>
          ) : null}
          <span className="price-spacer" />
          <button type="button" onClick={onClose} disabled={busy}>Cancel</button>
          <button type="button" className="price-save" onClick={() => void submit()} disabled={busy || state.model.trim() === ''}>
            {busy ? 'Saving…' : 'Save price'}
          </button>
        </div>
        {prices.length > 0 ? (
          <footer className="price-list">
            <h3>Saved rates</h3>
            {prices.map((price) => (
              <button key={price.model} type="button" className="price-entry" onClick={() => setState({
                model: price.model,
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

function normalizeDraft(state: EditorState, existing: ModelPrice | undefined): PriceDraft | null {
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
