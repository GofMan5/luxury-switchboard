import { FlaskConical } from 'lucide-react'
import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { ControlPlaneError } from '../../../shared/contracts/protocol'
import { Button } from '../../../shared/ui/Button'
import { RULE_LIMITS, ruleTestProblems, type RuleTestInput, type RuleTestResult } from '../domain/rules'
import type { ProviderOption } from './RequestRuleEditor'
import { focusFirstInvalid, problemFinder, testInput, type TesterForm } from './rule-forms'
import { describeEffects, type ProviderNames } from './rule-text'
import { Field } from './RuleFields'
import styles from './RulesPage.module.css'

/** Paths a client sends most, offered while typing. */
const COMMON_PATHS = ['/v1/chat/completions', '/v1/responses', '/v1/messages', '/v1/completions', '/v1/embeddings', '/v1/images/generations', '/v1/models'] as const

type Outcome =
  | { readonly kind: 'idle' }
  | { readonly kind: 'pending' }
  | { readonly kind: 'done'; readonly input: RuleTestInput; readonly result: RuleTestResult }
  | { readonly kind: 'error'; readonly message: string }

const hasError = (input: RuleTestInput): boolean => input.status !== undefined || Boolean(input.errorCode) || Boolean(input.errorText)

/**
 * Asks the control plane which rules a request (and, on the retry tab, a
 * failed attempt) would match. Nothing is sent upstream and no counter moves.
 */
export function RuleTester({ kind, ruleNames, providerNames, providers, onTest }: {
  readonly kind: 'request' | 'retry'
  /** Rule ids to names, of both kinds. */
  readonly ruleNames: ReadonlyMap<string, string>
  readonly providerNames: ProviderNames
  readonly providers: readonly ProviderOption[]
  readonly onTest: (input: RuleTestInput, signal: AbortSignal) => Promise<RuleTestResult>
}) {
  const [form, setForm] = useState<TesterForm>({ model: '', path: '/v1/chat/completions', status: kind === 'retry' ? '503' : '', errorCode: '', errorText: '', providerId: '' })
  const [tried, setTried] = useState(false)
  const [outcome, setOutcome] = useState<Outcome>({ kind: 'idle' })
  const formRef = useRef<HTMLFormElement>(null)
  const abortRef = useRef<AbortController | null>(null)
  const withError = kind === 'retry'

  useEffect(() => () => abortRef.current?.abort(), [])

  const input = useMemo(() => testInput(form, withError), [form, withError])
  const problems = useMemo(() => ruleTestProblems(input), [input])
  const problemFor = problemFinder(problems, tried)
  const update = (patch: Partial<TesterForm>) => setForm((current) => ({ ...current, ...patch }))

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    setTried(true)
    if (problems.length > 0) {
      focusFirstInvalid(formRef.current)
      return
    }
    abortRef.current?.abort()
    const controller = new AbortController()
    abortRef.current = controller
    setOutcome({ kind: 'pending' })
    try {
      const result = await onTest(input, controller.signal)
      if (!controller.signal.aborted) setOutcome({ kind: 'done', input, result })
    } catch (error) {
      if (!controller.signal.aborted) setOutcome({ kind: 'error', message: error instanceof ControlPlaneError ? error.message : 'The test could not run.' })
    }
  }

  return (
    <section className={styles.tester} aria-labelledby={`${kind}-tester-title`}>
      <header>
        <h3 id={`${kind}-tester-title`}>Test a request</h3>
        <p className={styles.muted}>{withError ? 'Describe a failed attempt to see which retry rule handles it.' : 'See which rules match and what the relay would change.'} Nothing is sent upstream.</p>
      </header>
      <form ref={formRef} className={styles.testerFields} noValidate onSubmit={(event) => void submit(event)}>
        <Field label="Model" error={problemFor('model')}>
          {(control) => <input {...control} className={styles.mono} value={form.model} placeholder="gpt-4o" spellCheck={false} autoComplete="off" onChange={(event) => update({ model: event.currentTarget.value })} />}
        </Field>
        <Field label="Path" error={problemFor('path')}>
          {(control) => (
            <>
              <input {...control} className={styles.mono} list={`${kind}-tester-paths`} value={form.path} maxLength={RULE_LIMITS.testPath} spellCheck={false} autoComplete="off" onChange={(event) => update({ path: event.currentTarget.value })} />
              <datalist id={`${kind}-tester-paths`}>{COMMON_PATHS.map((path) => <option key={path} value={path} />)}</datalist>
            </>
          )}
        </Field>
        {withError ? (
          <>
            <Field label="Status" error={problemFor('status')} hint="Blank for a transport error.">
              {(control) => <input {...control} className={styles.mono} inputMode="numeric" value={form.status} placeholder="503" autoComplete="off" onChange={(event) => update({ status: event.currentTarget.value })} />}
            </Field>
            <Field label="Error code" error={problemFor('errorCode')}>
              {(control) => <input {...control} className={styles.mono} value={form.errorCode} placeholder="upstream_status" spellCheck={false} autoComplete="off" onChange={(event) => update({ errorCode: event.currentTarget.value })} />}
            </Field>
            <Field label="Provider" error={problemFor('providerId')}>
              {(control) => (
                <select {...control} value={form.providerId} onChange={(event) => update({ providerId: event.currentTarget.value })}>
                  <option value="">Any provider</option>
                  {providers.map((provider) => <option key={provider.id} value={provider.id}>{provider.name}</option>)}
                </select>
              )}
            </Field>
            <Field label="Error text" className={styles.wide} error={problemFor('errorText')}>
              {(control) => <textarea {...control} rows={2} value={form.errorText} placeholder="The model is overloaded. Please try again later." spellCheck={false} onChange={(event) => update({ errorText: event.currentTarget.value })} />}
            </Field>
          </>
        ) : null}
        <div className={styles.testerActions}>
          <Button type="submit" disabled={outcome.kind === 'pending'}><FlaskConical size={14} />{outcome.kind === 'pending' ? 'Testing…' : 'Test'}</Button>
        </div>
      </form>
      <TestOutcome outcome={outcome} kind={kind} ruleNames={ruleNames} providerNames={providerNames} />
    </section>
  )
}

function TestOutcome({ outcome, kind, ruleNames, providerNames }: {
  readonly outcome: Outcome
  readonly kind: 'request' | 'retry'
  readonly ruleNames: ReadonlyMap<string, string>
  readonly providerNames: ProviderNames
}) {
  if (outcome.kind === 'idle') return null
  if (outcome.kind === 'pending') return <div className={styles.result} role="status" aria-live="polite">Testing…</div>
  if (outcome.kind === 'error') return <div className={styles.result} role="alert"><p className={styles.formError}>{outcome.message}</p></div>
  const { result, input } = outcome
  const name = (id: string) => ruleNames.get(id) ?? id
  const effects = describeEffects(result.effects, providerNames)
  return (
    <div className={styles.result} role="status" aria-live="polite">
      {kind === 'request' || result.requestRules.length > 0 ? (
        <dl>
          <dt>Request rules</dt>
          <dd>{result.requestRules.length ? result.requestRules.map(name).join(' → ') : <span className={styles.muted}>None match this request.</span>}</dd>
          {kind === 'request' ? (
            <>
              <dt>Effect</dt>
              <dd>{effects.length ? effects.join(' · ') : <span className={styles.muted}>Sent unchanged.</span>}</dd>
            </>
          ) : null}
        </dl>
      ) : null}
      {kind === 'retry' && hasError(input) ? (
        <dl>
          <dt>Retry rule</dt>
          <dd>{result.retryRule ? name(result.retryRule) : <span className={styles.muted}>No custom rule matches; the built-in handling applies.</span>}</dd>
        </dl>
      ) : null}
    </div>
  )
}
