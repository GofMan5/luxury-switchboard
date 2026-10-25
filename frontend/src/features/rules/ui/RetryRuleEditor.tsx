import { X } from 'lucide-react'
import { useMemo, useState, type FormEvent } from 'react'
import type { RetryRuleDraftSource } from '../../../shared/contracts/rules-link'
import { Button } from '../../../shared/ui/Button'
import { useModalFocus } from '../../../shared/ui/useModalFocus'
import { ownerPlugin, RETRY_ACTION_LABELS, RETRY_ACTIONS, RULE_LIMITS, retryRuleProblems, type RetryAction, type RetryRule } from '../domain/rules'
import type { MutationResult } from '../application/rules-model'
import type { ProviderOption } from './RequestRuleEditor'
import { COMMON_ERROR_CODES, editorProblems, focusFirstInvalid, problemFinder, retryRuleForm, retryRuleFromForm, suggestedId, type RetryRuleForm } from './rule-forms'
import { Field } from './RuleFields'
import styles from './RulesPage.module.css'

const ACTION_HINTS: Readonly<Record<RetryAction, string>> = {
  retry: 'Send the same request to the same provider again.',
  rotate_key: 'Try again with the provider’s next API key, as after a rejected key.',
  switch_provider: 'Try the next provider in the model’s route chain. Over the tunnel this retries in place.',
  fail: 'Stop and return the error to the client without retrying.',
}

export function RetryRuleEditor({ rule, draft, takenIds, providers, onClose, onSave }: {
  /** null for a new rule. */
  readonly rule: RetryRule | null
  /** A failed request this new rule starts from. */
  readonly draft?: RetryRuleDraftSource | null
  readonly takenIds: ReadonlySet<string>
  readonly providers: readonly ProviderOption[]
  readonly onClose: () => void
  readonly onSave: (rule: RetryRule) => Promise<MutationResult>
}) {
  const [form, setForm] = useState<RetryRuleForm>(() => {
    const initial = retryRuleForm(rule, draft)
    return rule ? initial : { ...initial, id: suggestedId(initial.name, takenIds) }
  })
  const [idEdited, setIdEdited] = useState(false)
  const [tried, setTried] = useState(false)
  const [pending, setPending] = useState(false)
  const [refusal, setRefusal] = useState('')
  const formRef = useModalFocus<HTMLFormElement>(onClose, pending)
  const isNew = rule === null
  const plugin = ownerPlugin(rule?.owner)

  const candidate = useMemo(() => retryRuleFromForm(form), [form])
  const problems = useMemo(() => editorProblems(retryRuleProblems(candidate), { idTaken: isNew && takenIds.has(candidate.id) }), [candidate, isNew, takenIds])
  const problemFor = problemFinder(problems, tried)

  const update = (patch: Partial<RetryRuleForm>) => { setForm((current) => ({ ...current, ...patch })); setRefusal('') }
  const rename = (name: string) => update(isNew && !idEdited ? { name, id: suggestedId(name, takenIds) } : { name })
  const toggleProvider = (id: string, on: boolean) =>
    update({ providers: on ? [...form.providers, id] : form.providers.filter((entry) => entry !== id) })

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

  // Providers the rule names that no longer exist stay listed so they can be unticked.
  const providerOptions = [
    ...providers,
    ...form.providers.filter((id) => !providers.some((provider) => provider.id === id)).map((id) => ({ id, name: `${id} (not configured)` })),
  ]
  const matchProblem = problemFor('match')
  const fail = form.action === 'fail'

  return (
    <div className="ui-scrim">
      <form ref={formRef} className={`ui-modal ${styles.editorModal}`} role="dialog" aria-modal="true" aria-label={isNew ? 'New retry rule' : `Edit ${rule.name}`} noValidate onSubmit={(event) => void submit(event)}>
        <header>
          <div>
            <h2>{isNew ? 'New retry rule' : 'Edit retry rule'}</h2>
            <p>The first enabled rule that matches a failed attempt decides what happens next. Errors no rule matches keep the built-in handling.</p>
          </div>
          <button type="button" aria-label="Close" disabled={pending} onClick={onClose}><X size={16} /></button>
        </header>
        <div className={styles.form}>
          {plugin ? <p className={styles.note}>Added by the plugin <strong>{plugin}</strong>. Your changes keep it owned by the plugin, which can change it again.</p> : null}
          {draft && isNew ? <p className={styles.note}>Prefilled from a failed request. Narrow the phrase to the part that identifies this error.</p> : null}
          <fieldset className={styles.section}>
            <legend>Rule</legend>
            <div className={styles.pair}>
              <Field label="Name" error={problemFor('name')}>
                {(control) => <input {...control} data-autofocus value={form.name} autoComplete="off" onChange={(event) => rename(event.currentTarget.value)} />}
              </Field>
              <Field label="Id" error={problemFor('id')} hint={isNew ? 'Picked from the name.' : 'An id cannot change.'}>
                {(control) => <input {...control} className={styles.mono} value={form.id} readOnly={!isNew} spellCheck={false} autoComplete="off" onChange={(event) => { setIdEdited(true); update({ id: event.currentTarget.value }) }} />}
              </Field>
            </div>
            <label className={styles.check}><input type="checkbox" checked={form.enabled} onChange={(event) => update({ enabled: event.currentTarget.checked })} />Enabled</label>
          </fieldset>

          <fieldset className={styles.section}>
            <legend>Matches a failure with</legend>
            <p className={styles.sectionHint}>Every filled field must match. Set at least one of status, phrase or error code.</p>
            <div className={styles.pair}>
              <Field label="HTTP statuses" error={problemFor('statuses', 'match')} hint="Comma-separated, 400–599.">
                {(control) => <input {...control} className={styles.mono} inputMode="numeric" value={form.statuses} placeholder="502, 503" autoComplete="off" onChange={(event) => update({ statuses: event.currentTarget.value })} />}
              </Field>
              <Field label="Error codes" error={problemFor('errorCodes')} hint={<>Such as {COMMON_ERROR_CODES.map((code, index) => <span key={code}>{index ? ', ' : ''}<code>{code}</code></span>)}.</>}>
                {(control) => <input {...control} className={styles.mono} value={form.errorCodes} placeholder="upstream_status" spellCheck={false} autoComplete="off" onChange={(event) => update({ errorCodes: event.currentTarget.value })} />}
              </Field>
            </div>
            <Field label="Error text contains" error={problemFor('contains')} hint={`One phrase per line, ${RULE_LIMITS.containsMin}–${RULE_LIMITS.containsMax} characters. Case, - and _ are ignored.`}>
              {(control) => <textarea {...control} rows={2} value={form.contains} placeholder="model is overloaded" spellCheck={false} onChange={(event) => update({ contains: event.currentTarget.value })} />}
            </Field>
            {matchProblem ? <small className={styles.fieldError} role="status">{matchProblem}</small> : null}
            <Field label="Only for models" error={problemFor('retryModels')} hint="Comma-separated; * matches any text. Blank: every model.">
              {(control) => <input {...control} className={styles.mono} value={form.models} placeholder="claude-*" spellCheck={false} autoComplete="off" onChange={(event) => update({ models: event.currentTarget.value })} />}
            </Field>
            {providerOptions.length > 0 ? (
              <fieldset className={styles.checks}>
                <legend>Only from providers <span className={styles.muted}>(none ticked: every provider)</span></legend>
                {providerOptions.map((provider) => (
                  <label key={provider.id} className={styles.check}>
                    <input type="checkbox" checked={form.providers.includes(provider.id)} disabled={!form.providers.includes(provider.id) && form.providers.length >= RULE_LIMITS.providers} onChange={(event) => toggleProvider(provider.id, event.currentTarget.checked)} />
                    {provider.name}
                  </label>
                ))}
              </fieldset>
            ) : null}
            {problemFor('providers') ? <small className={styles.fieldError}>{problemFor('providers')}</small> : null}
          </fieldset>

          <fieldset className={styles.section}>
            <legend>Then</legend>
            <Field label="Action" error={problemFor('action')} hint={ACTION_HINTS[form.action]}>
              {(control) => (
                <select {...control} value={form.action} onChange={(event) => update({ action: event.currentTarget.value as RetryAction })}>
                  {RETRY_ACTIONS.map((action) => <option key={action} value={action}>{RETRY_ACTION_LABELS[action]}</option>)}
                </select>
              )}
            </Field>
            {fail ? null : (
              <div className={styles.pair}>
                <Field label="Attempts" error={problemFor('maxAttempts')} hint={`1–${RULE_LIMITS.maxAttempts}, counting only the retries this rule adds.`}>
                  {(control) => <input {...control} type="number" inputMode="numeric" min={1} max={RULE_LIMITS.maxAttempts} value={form.maxAttempts} onChange={(event) => update({ maxAttempts: event.currentTarget.value })} />}
                </Field>
                <Field label="Delay between attempts" error={problemFor('delayMs')}>
                  {(control) => (
                    <div className={styles.inline}>
                      <input {...control} type="number" inputMode="numeric" min={0} max={RULE_LIMITS.delayMs} step={100} value={form.delayMs} onChange={(event) => update({ delayMs: event.currentTarget.value })} />
                      <span className={styles.muted}>ms</span>
                    </div>
                  )}
                </Field>
              </div>
            )}
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
