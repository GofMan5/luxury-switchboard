import { Plus, Trash2, X } from 'lucide-react'
import { useMemo, useState, type FormEvent } from 'react'
import { Button } from '../../../shared/ui/Button'
import { useModalFocus } from '../../../shared/ui/useModalFocus'
import {
  BLOCK_STATUSES, BUDGET_PERIODS, ENDPOINT_LABELS, ENDPOINTS, ownerPlugin, RULE_LIMITS, requestRuleProblems,
  type Endpoint, type RequestRule,
} from '../domain/rules'
import type { MutationResult } from '../application/rules-model'
import {
  duplicateHeader, editorProblems, focusFirstInvalid, headerRow, isBlockStatus, problemFinder, requestRuleForm, requestRuleFromForm, suggestedId, WINDOW_UNITS,
  type RequestRuleForm, type WindowUnit,
} from './rule-forms'
import { Field } from './RuleFields'
import styles from './RulesPage.module.css'

export interface ProviderOption {
  readonly id: string
  readonly name: string
}

export function RequestRuleEditor({ rule, takenIds, providers, onClose, onSave }: {
  /** null for a new rule. */
  readonly rule: RequestRule | null
  /** Ids of every other rule, of both kinds. */
  readonly takenIds: ReadonlySet<string>
  readonly providers: readonly ProviderOption[]
  readonly onClose: () => void
  readonly onSave: (rule: RequestRule) => Promise<MutationResult>
}) {
  const [form, setForm] = useState<RequestRuleForm>(() => requestRuleForm(rule))
  const [idEdited, setIdEdited] = useState(false)
  const [tried, setTried] = useState(false)
  const [pending, setPending] = useState(false)
  const [refusal, setRefusal] = useState('')
  const formRef = useModalFocus<HTMLFormElement>(onClose, pending)
  const isNew = rule === null
  const plugin = ownerPlugin(rule?.owner)

  const candidate = useMemo(() => requestRuleFromForm(form), [form])
  const problems = useMemo(() => editorProblems(requestRuleProblems(candidate), {
    duplicateHeader: duplicateHeader(form),
    idTaken: isNew && takenIds.has(candidate.id),
  }), [candidate, form, isNew, takenIds])
  const problemFor = problemFinder(problems, tried)

  const update = (patch: Partial<RequestRuleForm>) => { setForm((current) => ({ ...current, ...patch })); setRefusal('') }
  const rename = (name: string) => update(isNew && !idEdited ? { name, id: suggestedId(name, takenIds) } : { name })
  const toggleEndpoint = (endpoint: Endpoint, on: boolean) =>
    update({ endpoints: ENDPOINTS.filter((entry) => (entry === endpoint ? on : form.endpoints.includes(entry))) })
  const setHeader = (key: number, patch: { name?: string; value?: string }) =>
    update({ headers: form.headers.map((row) => (row.key === key ? { ...row, ...patch } : row)) })

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (pending) return
    setTried(true)
    if (problems.length > 0) {
      focusFirstInvalid(formRef.current)
      return
    }
    setPending(true)
    const result = await onSave(candidate)
    setPending(false)
    if (result.ok) onClose()
    else setRefusal(result.message)
  }

  // A provider the rule names but this list lacks (deleted since) stays selectable, marked.
  const providerOptions = form.provider && !providers.some((provider) => provider.id === form.provider)
    ? [...providers, { id: form.provider, name: `${form.provider} (not configured)` }]
    : providers
  const headerProblem = problemFor('headers')

  return (
    <div className="ui-scrim">
      <form ref={formRef} className={`ui-modal ${styles.editorModal}`} role="dialog" aria-modal="true" aria-label={isNew ? 'New request rule' : `Edit ${rule.name}`} noValidate onSubmit={(event) => void submit(event)}>
        <header>
          <div>
            <h2>{isNew ? 'New request rule' : 'Edit request rule'}</h2>
            <p>Matching requests get every effect below, in list order.</p>
          </div>
          <button type="button" aria-label="Close" disabled={pending} onClick={onClose}><X size={16} /></button>
        </header>
        <div className={styles.form}>
          {plugin ? <p className={styles.note}>Added by the plugin <strong>{plugin}</strong>. Your changes keep it owned by the plugin, which can change it again.</p> : null}
          <fieldset className={styles.section}>
            <legend>Rule</legend>
            <div className={styles.pair}>
              <Field label="Name" error={problemFor('name')}>
                {(control) => <input {...control} data-autofocus value={form.name} maxLength={RULE_LIMITS.name * 2} autoComplete="off" onChange={(event) => rename(event.currentTarget.value)} />}
              </Field>
              <Field label="Id" error={problemFor('id')} hint={isNew ? 'Picked from the name. Plugins and the tester refer to it.' : 'An id cannot change.'}>
                {(control) => <input {...control} className={styles.mono} value={form.id} readOnly={!isNew} spellCheck={false} autoComplete="off" onChange={(event) => { setIdEdited(true); update({ id: event.currentTarget.value }) }} />}
              </Field>
            </div>
            <label className={styles.check}><input type="checkbox" checked={form.enabled} onChange={(event) => update({ enabled: event.currentTarget.checked })} />Enabled</label>
          </fieldset>

          <fieldset className={styles.section}>
            <legend>Applies to</legend>
            <Field label="Models" error={problemFor('models')} hint="Comma-separated; * matches any text, ignoring case. Blank matches every model.">
              {(control) => <input {...control} className={styles.mono} value={form.models} placeholder="gpt-4*, claude-*" spellCheck={false} autoComplete="off" onChange={(event) => update({ models: event.currentTarget.value })} />}
            </Field>
            <fieldset className={styles.checks} aria-describedby={problemFor('endpoints') ? 'request-endpoints-error' : undefined}>
              <legend>Endpoints <span className={styles.muted}>(none ticked: every endpoint)</span></legend>
              {ENDPOINTS.map((endpoint) => (
                <label key={endpoint} className={styles.check}>
                  <input type="checkbox" checked={form.endpoints.includes(endpoint)} onChange={(event) => toggleEndpoint(endpoint, event.currentTarget.checked)} />
                  {ENDPOINT_LABELS[endpoint]}
                </label>
              ))}
            </fieldset>
            {problemFor('endpoints') ? <small id="request-endpoints-error" className={styles.fieldError}>{problemFor('endpoints')}</small> : null}
          </fieldset>

          <fieldset className={styles.section}>
            <legend>Effects</legend>
            <p className={styles.sectionHint}>The first matching rule that picks a provider, a model or a block wins; tags and headers add up.</p>
            <div className={styles.pair}>
              <Field label="Provider" error={problemFor('provider')}>
                {(control) => (
                  <select {...control} value={form.provider} onChange={(event) => update({ provider: event.currentTarget.value })}>
                    <option value="">Keep the routed provider</option>
                    {providerOptions.map((provider) => <option key={provider.id} value={provider.id}>{provider.name}</option>)}
                  </select>
                )}
              </Field>
              <Field label="Upstream model" error={problemFor('model')} hint="Blank keeps the requested model.">
                {(control) => <input {...control} className={styles.mono} value={form.model} spellCheck={false} autoComplete="off" onChange={(event) => update({ model: event.currentTarget.value })} />}
              </Field>
            </div>

            <label className={styles.check}><input type="checkbox" checked={form.blockOn} onChange={(event) => update({ blockOn: event.currentTarget.checked })} />Block matching requests</label>
            {form.blockOn ? (
              <div className={styles.pair}>
                <Field label="Answer with" error={problemFor('blockStatus')}>
                  {(control) => (
                    <select {...control} value={form.blockStatus} onChange={(event) => { const status = Number(event.currentTarget.value); if (isBlockStatus(status)) update({ blockStatus: status }) }}>
                      {BLOCK_STATUSES.map((status) => <option key={status} value={status}>{status}</option>)}
                    </select>
                  )}
                </Field>
                <Field label="Message" error={problemFor('blockMessage')} hint="The client sees this text.">
                  {(control) => <input {...control} value={form.blockMessage} placeholder="This model is not available here." autoComplete="off" onChange={(event) => update({ blockMessage: event.currentTarget.value })} />}
                </Field>
              </div>
            ) : null}

            <label className={styles.check}><input type="checkbox" checked={form.rateOn} onChange={(event) => update({ rateOn: event.currentTarget.checked })} />Limit the request rate</label>
            {form.rateOn ? (
              <div className={styles.pair}>
                <Field label="Requests" error={problemFor('rateRequests')} hint="Extra requests get 429 until the window ends.">
                  {(control) => <input {...control} type="number" inputMode="numeric" min={1} max={RULE_LIMITS.rateRequests} value={form.rateRequests} onChange={(event) => update({ rateRequests: event.currentTarget.value })} />}
                </Field>
                <Field label="Per" error={problemFor('rateWindow')}>
                  {(control) => (
                    <div className={styles.inline}>
                      <input {...control} type="number" inputMode="numeric" min={1} value={form.rateWindow} onChange={(event) => update({ rateWindow: event.currentTarget.value })} />
                      <select aria-label="Window unit" value={form.rateUnit} onChange={(event) => update({ rateUnit: event.currentTarget.value as WindowUnit })}>
                        {WINDOW_UNITS.map((unit) => <option key={unit.id} value={unit.id}>{unit.label}</option>)}
                      </select>
                    </div>
                  )}
                </Field>
              </div>
            ) : null}

            <label className={styles.check}><input type="checkbox" checked={form.budgetOn} onChange={(event) => update({ budgetOn: event.currentTarget.checked })} />Cap usage with a budget</label>
            {form.budgetOn ? (
              <>
                <div className={styles.triple}>
                  <Field label="Requests" error={problemFor('budgetRequests', 'budget')}>
                    {(control) => <input {...control} type="number" inputMode="numeric" min={1} max={RULE_LIMITS.budgetRequests} value={form.budgetRequests} placeholder="No cap" onChange={(event) => update({ budgetRequests: event.currentTarget.value })} />}
                  </Field>
                  <Field label="Tokens" error={problemFor('budgetTokens')}>
                    {(control) => <input {...control} type="number" inputMode="numeric" min={1} max={RULE_LIMITS.budgetTokens} value={form.budgetTokens} placeholder="No cap" onChange={(event) => update({ budgetTokens: event.currentTarget.value })} />}
                  </Field>
                  <Field label="Resets every" error={problemFor('budgetPeriod')}>
                    {(control) => (
                      <select {...control} value={form.budgetPeriod} onChange={(event) => update({ budgetPeriod: event.currentTarget.value as RequestRuleForm['budgetPeriod'] })}>
                        {BUDGET_PERIODS.map((period) => <option key={period} value={period}>{period}</option>)}
                      </select>
                    )}
                  </Field>
                </div>
                <p className={styles.sectionHint}>Counted in local time. Once spent, matching requests get 429 until the next {form.budgetPeriod}.</p>
              </>
            ) : null}

            <fieldset className={styles.checks} style={{ gridTemplateColumns: 'minmax(0, 1fr)' }}>
              <legend>Headers sent upstream</legend>
              <div className={styles.headerRows}>
                {form.headers.map((row, index) => (
                  <div key={row.key} className={styles.headerRow}>
                    <input aria-label={`Header ${index + 1} name`} aria-invalid={headerProblem ? true : undefined} className={styles.mono} value={row.name} placeholder="X-Team" spellCheck={false} autoComplete="off" onChange={(event) => setHeader(row.key, { name: event.currentTarget.value })} />
                    <input aria-label={`Header ${index + 1} value`} className={styles.mono} value={row.value} spellCheck={false} autoComplete="off" onChange={(event) => setHeader(row.key, { value: event.currentTarget.value })} />
                    <button type="button" className={styles.iconButton} data-danger="" aria-label={`Remove header ${index + 1}`} onClick={() => update({ headers: form.headers.filter((entry) => entry.key !== row.key) })}><Trash2 /></button>
                  </div>
                ))}
                <Button type="button" variant="ghost" className={styles.addRow} disabled={form.headers.length >= RULE_LIMITS.headers} onClick={() => update({ headers: [...form.headers, headerRow()] })}>
                  <Plus size={14} /> Add header
                </Button>
              </div>
            </fieldset>
            {headerProblem ? <small className={styles.fieldError}>{headerProblem}</small> : <small className={styles.sectionHint}>Names start with X-. X-Switchboard-*, X-Forwarded-*, X-Real-IP, X-Stainless-* and credential-like names are reserved; a header already on the request is never replaced.</small>}

            <Field label="Tags" error={problemFor('tags')} hint="Comma-separated; shown on matching requests in Live Activity.">
              {(control) => <input {...control} className={styles.mono} value={form.tags} placeholder="team-a, batch" spellCheck={false} autoComplete="off" onChange={(event) => update({ tags: event.currentTarget.value })} />}
            </Field>
            <label className={styles.check}><input type="checkbox" checked={form.stop} onChange={(event) => update({ stop: event.currentTarget.checked })} />Stop here: rules below are not evaluated for matching requests</label>
          </fieldset>
          {refusal ? <p className={styles.formError} role="alert">{refusal}</p> : null}
          {tried && problems.length > 0 ? <p className={styles.formError} role="alert">{problems.length === 1 ? 'Fix the highlighted field to save.' : `Fix the ${problems.length} highlighted problems to save.`}</p> : null}
        </div>
        <footer>
          <Button type="button" disabled={pending} onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={pending}>{pending ? 'Saving…' : 'Save rule'}</Button>
        </footer>
      </form>
    </div>
  )
}
