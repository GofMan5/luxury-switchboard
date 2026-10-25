import { AlertTriangle, ListFilter, Plus, RefreshCw, RotateCcw, X } from 'lucide-react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { cleanDraft, parseRulesHash, rulesTabHash, type RetryRuleDraftSource, type RulesTab } from '../../../shared/contracts/rules-link'
import { Button } from '../../../shared/ui/Button'
import { EmptyState, Panel, Segmented } from '../../../shared/ui/chrome'
import { useModalFocus } from '../../../shared/ui/useModalFocus'
import { useProviders } from '../../providers/ui/useProviders'
import { RULE_LIMITS, type RecentError, type RequestRule, type RetryRule } from '../domain/rules'
import { RecentErrorsTable } from './RecentErrorsTable'
import { RequestRuleEditor, type ProviderOption } from './RequestRuleEditor'
import { RetryRuleEditor } from './RetryRuleEditor'
import { RuleList, type RuleRowView } from './RuleList'
import { RuleTester } from './RuleTester'
import { describeEffects, describeRequestMatch, describeRetryAction, describeRetryMatch, requestRuleCounters, retryRuleCounters, type ProviderNames } from './rule-text'
import { useRules } from './useRules'
import styles from './RulesPage.module.css'

/** How often the hit and budget counters follow live traffic. */
const STATS_REFRESH_MS = 5_000

type Editor =
  | { readonly kind: 'request'; readonly rule: RequestRule | null }
  | { readonly kind: 'retry'; readonly rule: RetryRule | null; readonly draft?: RetryRuleDraftSource | null }

type Deleting = { readonly kind: 'request' | 'retry'; readonly id: string; readonly name: string }

const TABS: readonly { id: RulesTab; label: string }[] = [
  { id: 'request', label: 'Request rules' },
  { id: 'retry', label: 'Retry rules' },
  { id: 'errors', label: 'Recent errors' },
]

/** The tab a hash names, and a new-rule draft once (the hash is then cleared of it). */
function useRulesLink(): [RulesTab, (tab: RulesTab) => void, RetryRuleDraftSource | null, () => void] {
  const [link, setLink] = useState(() => parseRulesHash(window.location.hash) ?? { tab: 'request' as const, draft: null })
  useEffect(() => {
    const update = () => { const next = parseRulesHash(window.location.hash); if (next) setLink(next) }
    window.addEventListener('hashchange', update)
    return () => window.removeEventListener('hashchange', update)
  }, [])
  const select = useCallback((tab: RulesTab) => {
    setLink({ tab, draft: null })
    const hash = rulesTabHash(tab)
    if (window.location.hash !== hash) window.history.replaceState(null, '', hash)
  }, [])
  // A draft opens the editor once; replacing the hash keeps Back from reopening it.
  const consume = useCallback(() => {
    setLink((current) => ({ ...current, draft: null }))
    window.history.replaceState(null, '', rulesTabHash('retry'))
  }, [])
  return [link.tab, select, link.draft, consume]
}

export default function RulesPage() {
  const { state, model } = useRules()
  const { state: providerState } = useProviders()
  const [tab, setTab, linkDraft, consumeDraft] = useRulesLink()
  const [editor, setEditor] = useState<Editor | null>(null)
  const [deleting, setDeleting] = useState<Deleting | null>(null)

  const providers = useMemo<readonly ProviderOption[]>(
    () => providerState.catalog.providers.map((provider) => ({ id: provider.id, name: provider.name })),
    [providerState.catalog.providers],
  )
  const providerNames = useMemo<ProviderNames>(() => new Map(providers.map((provider) => [provider.id, provider.name])), [providers])
  const ruleNames = useMemo(() => new Map([...state.requestRules, ...state.retryRules].map((rule) => [rule.id, rule.name])), [state.requestRules, state.retryRules])
  const retryNames = useMemo(() => new Map(state.retryRules.map((rule) => [rule.id, rule.name])), [state.retryRules])
  const takenIds = useMemo(() => new Set(ruleNames.keys()), [ruleNames])

  // The workspace loads on first visit; reconnects are handled by the services.
  useEffect(() => { if (state.phase === 'idle') void model.load() }, [model, state.phase])
  useEffect(() => {
    if (state.phase !== 'ready' || tab === 'errors') return undefined
    const timer = window.setInterval(() => { if (document.visibilityState === 'visible') void model.refreshStats() }, STATS_REFRESH_MS)
    return () => window.clearInterval(timer)
  }, [model, state.phase, tab])
  useEffect(() => { if (tab === 'errors' && state.phase === 'ready' && state.errorsPhase === 'idle') void model.loadErrors() }, [model, state.errorsPhase, state.phase, tab])

  // A link from a failed request opens a prefilled editor once the rules are known.
  useEffect(() => {
    if (!linkDraft || state.phase !== 'ready') return
    consumeDraft()
    if (state.retryRules.length < RULE_LIMITS.rules) setEditor({ kind: 'retry', rule: null, draft: linkDraft })
  }, [consumeDraft, linkDraft, state.phase, state.retryRules.length])

  const openEditor = (next: Editor) => { model.clearError(); setEditor(next) }
  const createFromError = (error: RecentError) => {
    openEditor({ kind: 'retry', rule: null, draft: cleanDraft({ status: error.status, errorCode: error.errorCode, detail: error.detail }) })
  }

  const requestRows = useMemo<RuleRowView[]>(() => state.requestRules.map((rule) => ({
    id: rule.id, name: rule.name, enabled: rule.enabled, owner: rule.owner,
    match: describeRequestMatch(rule),
    effects: describeEffects(rule.effects, providerNames).map((text) => ({ text, danger: text.startsWith('Block') })),
    counters: requestRuleCounters(rule, state.stats[rule.id]),
  })), [providerNames, state.requestRules, state.stats])
  const retryRows = useMemo<RuleRowView[]>(() => state.retryRules.map((rule) => ({
    id: rule.id, name: rule.name, enabled: rule.enabled, owner: rule.owner,
    match: describeRetryMatch(rule, providerNames),
    effects: [{ text: describeRetryAction(rule), danger: rule.action === 'fail' }],
    counters: retryRuleCounters(state.stats[rule.id]),
  })), [providerNames, state.retryRules, state.stats])

  const requestFull = state.requestRules.length >= RULE_LIMITS.rules
  const retryFull = state.retryRules.length >= RULE_LIMITS.rules
  const ready = state.phase === 'ready'

  const headerAction = tab === 'request'
    ? <Button variant="primary" disabled={!ready || requestFull} title={requestFull ? `At most ${RULE_LIMITS.rules} request rules.` : undefined} onClick={() => openEditor({ kind: 'request', rule: null })}><Plus size={16} />New request rule</Button>
    : tab === 'retry'
      ? <Button variant="primary" disabled={!ready || retryFull} title={retryFull ? `At most ${RULE_LIMITS.rules} retry rules.` : undefined} onClick={() => openEditor({ kind: 'retry', rule: null })}><Plus size={16} />New retry rule</Button>
      : <Button disabled={!ready || state.errorsPhase === 'loading'} onClick={() => void model.loadErrors()}><RefreshCw size={16} />{state.errorsPhase === 'loading' ? 'Loading…' : 'Refresh'}</Button>

  return (
    <section className={styles.page}>
      <header className="page-header">
        <div><h1>Rules</h1><p>Shape requests before they leave the relay, and decide how failed attempts are retried</p></div>
        <div className={styles.headerActions}>{headerAction}</div>
      </header>

      <div className={styles.toolbar}>
        <Segmented label="Rules view" value={tab} options={TABS} onChange={setTab} />
        <p>{TAB_HINTS[tab]}</p>
      </div>

      {state.error ? (
        <div className={styles.error} role="alert">
          <span>{state.error}</span>
          <button type="button" className={styles.iconButton} aria-label="Dismiss" onClick={() => model.clearError()}><X /></button>
        </div>
      ) : null}

      {state.phase === 'error' ? (
        <div className={styles.single}>
          <Panel title="Rules">
            <div className={styles.unavailable} role="alert">
              <AlertTriangle size={22} aria-hidden="true" />
              <p>{state.loadError}</p>
              <Button onClick={() => void model.load()}><RotateCcw size={14} />Try again</Button>
            </div>
          </Panel>
        </div>
      ) : !ready ? (
        <div className={styles.single}><Panel title="Rules"><div className={styles.loading} role="status">Loading rules…</div></Panel></div>
      ) : tab === 'request' ? (
        <div className={styles.layout}>
          <Panel title="Request rules" subtitle="Evaluated top to bottom for every relay request">
            {requestRows.length ? (
              <RuleList
                kind="Request"
                rows={requestRows}
                pending={state.pending}
                onMove={(id, direction) => model.moveRequestRule(id, direction)}
                onToggle={(id, enabled) => { const rule = state.requestRules.find((entry) => entry.id === id); if (rule) void model.setRequestRuleEnabled(rule, enabled) }}
                onEdit={(id) => { const rule = state.requestRules.find((entry) => entry.id === id); if (rule) openEditor({ kind: 'request', rule }) }}
                onDelete={(id) => setDeleting({ kind: 'request', id, name: ruleNames.get(id) ?? id })}
                footer={`${requestRows.length} of ${RULE_LIMITS.rules} rules · counters follow live traffic`}
              />
            ) : <EmptyState icon={ListFilter} title="No request rules" hint="Add a rule to route, rename, block, rate-limit or budget matching requests." />}
          </Panel>
          <Panel title="Tester">
            <RuleTester kind="request" ruleNames={ruleNames} providerNames={providerNames} providers={providers} onTest={(input, signal) => model.test(input, signal)} />
          </Panel>
        </div>
      ) : tab === 'retry' ? (
        <div className={styles.layout}>
          <Panel title="Retry rules" subtitle="Checked before the built-in handling, first match wins">
            {retryRows.length ? (
              <RuleList
                kind="Retry"
                rows={retryRows}
                pending={state.pending}
                onMove={(id, direction) => model.moveRetryRule(id, direction)}
                onToggle={(id, enabled) => { const rule = state.retryRules.find((entry) => entry.id === id); if (rule) void model.setRetryRuleEnabled(rule, enabled) }}
                onEdit={(id) => { const rule = state.retryRules.find((entry) => entry.id === id); if (rule) openEditor({ kind: 'retry', rule }) }}
                onDelete={(id) => setDeleting({ kind: 'retry', id, name: ruleNames.get(id) ?? id })}
                footer={`${retryRows.length} of ${RULE_LIMITS.rules} rules · built-in handling covers everything else`}
              />
            ) : <EmptyState icon={RotateCcw} title="No custom retry rules" hint="The built-in handling retries transport errors, 429 and 5xx. Add a rule for errors it treats differently." />}
          </Panel>
          <Panel title="Tester">
            <RuleTester kind="retry" ruleNames={ruleNames} providerNames={providerNames} providers={providers} onTest={(input, signal) => model.test(input, signal)} />
          </Panel>
        </div>
      ) : (
        <div className={styles.single}>
          <Panel title="Recent errors" subtitle="Failed relay requests, grouped by error, newest first">
            {state.errorsPhase === 'error' ? (
              <div className={styles.unavailable} role="alert">
                <AlertTriangle size={22} aria-hidden="true" />
                <p>{state.errorsMessage}</p>
                <Button onClick={() => void model.loadErrors()}><RotateCcw size={14} />Try again</Button>
              </div>
            ) : state.errorsPhase !== 'ready' ? (
              <div className={styles.loading} role="status">Loading recent errors…</div>
            ) : state.recentErrors.length ? (
              <RecentErrorsTable
                errors={state.recentErrors}
                ruleNames={retryNames}
                canCreate={!retryFull}
                onCreate={createFromError}
                onOpenRule={(id) => { const rule = state.retryRules.find((entry) => entry.id === id); if (rule) openEditor({ kind: 'retry', rule }) }}
              />
            ) : <EmptyState icon={ListFilter} title="No recent errors" hint="Failed relay requests appear here, with a shortcut to a retry rule for them." />}
          </Panel>
        </div>
      )}

      {editor?.kind === 'request' ? (
        <RequestRuleEditor
          rule={editor.rule}
          takenIds={editorTakenIds(takenIds, editor.rule)}
          providers={providers}
          onClose={() => setEditor(null)}
          onSave={(rule) => model.saveRequestRule(rule)}
        />
      ) : null}
      {editor?.kind === 'retry' ? (
        <RetryRuleEditor
          rule={editor.rule}
          draft={editor.draft ?? null}
          takenIds={editorTakenIds(takenIds, editor.rule)}
          providers={providers}
          onClose={() => setEditor(null)}
          onSave={(rule) => model.saveRetryRule(rule)}
        />
      ) : null}
      {deleting ? (
        <DeleteRuleDialog
          name={deleting.name}
          kind={deleting.kind}
          pending={state.pending === deleting.id}
          onClose={() => setDeleting(null)}
          onConfirm={async () => {
            const done = deleting.kind === 'request' ? await model.deleteRequestRule(deleting.id) : await model.deleteRetryRule(deleting.id)
            // A refusal is shown above the list; the dialog closes either way.
            setDeleting(null)
            return done
          }}
        />
      ) : null}
    </section>
  )
}

const TAB_HINTS: Readonly<Record<RulesTab, string>> = {
  request: 'Every matching rule applies; a provider, model or block is taken from the first rule that sets it.',
  retry: 'A failed attempt goes to the first enabled rule that matches it; unmatched errors keep the built-in handling.',
  errors: 'Create a retry rule from an error, or see which rule already handles it.',
}

/** Every id but the rule's own: an edited rule keeps its id. */
function editorTakenIds(taken: ReadonlySet<string>, rule: { id: string } | null): ReadonlySet<string> {
  if (!rule) return taken
  const others = new Set(taken)
  others.delete(rule.id)
  return others
}

function DeleteRuleDialog({ name, kind, pending, onClose, onConfirm }: {
  readonly name: string
  readonly kind: 'request' | 'retry'
  readonly pending: boolean
  readonly onClose: () => void
  readonly onConfirm: () => Promise<boolean>
}) {
  const dialogRef = useModalFocus<HTMLElement>(onClose, pending)
  return (
    <div className="ui-scrim">
      <section ref={dialogRef} className="ui-modal" role="alertdialog" aria-modal="true" aria-labelledby="delete-rule-title" aria-describedby="delete-rule-body">
        <header>
          <div>
            <h2 id="delete-rule-title" className={styles.confirmName}>Delete “{name}”?</h2>
            <p id="delete-rule-body">{kind === 'request' ? 'Matching requests are no longer changed by this rule. Its counters are dropped.' : 'Errors it handled go to the next matching rule, or the built-in handling.'}</p>
          </div>
        </header>
        <footer>
          <Button type="button" data-autofocus disabled={pending} onClick={onClose}>Cancel</Button>
          <Button type="button" variant="danger" disabled={pending} onClick={() => void onConfirm()}>{pending ? 'Deleting…' : 'Delete rule'}</Button>
        </footer>
      </section>
    </div>
  )
}
