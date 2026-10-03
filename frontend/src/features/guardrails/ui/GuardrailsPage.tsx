import { useState } from 'react'
import { RefreshCw, ScanEye, ShieldAlert, ShieldCheck, ShieldOff, Trash2, X } from 'lucide-react'
import { formatClock } from '../../../shared/format/metrics'
import { Button } from '../../../shared/ui/Button'
import { Metric, MetricStrip } from '../../../shared/ui/chrome'
import { StatusDot } from '../../../shared/ui/StatusDot'
import { useModalFocus } from '../../../shared/ui/useModalFocus'
import {
  GUARDRAIL_MODES,
  modeDescription,
  modeLabel,
  type GuardrailFinding,
  type GuardrailRecord,
  type GuardrailSeverity,
} from '../domain/guardrail'
import { useGuardrails } from './useGuardrails'
import styles from './GuardrailsPage.module.css'

export default function GuardrailsPage() {
  const { model, state } = useGuardrails()
  const [openId, setOpenId] = useState('')
  const status = state.status
  const mode = status?.mode ?? 'monitor'
  const counts = state.findings.reduce(
    (total, record) => {
      if (record.verdict === 'blocked') total.blocked++
      if (record.severity === 'high') total.high++
      total.providers.add(record.providerId || record.providerName)
      return total
    },
    { blocked: 0, high: 0, providers: new Set<string>() },
  )
  const selected = state.findings.find((record) => record.id === openId)
  const withheld = Math.max((status?.findingCount ?? 0) - state.findings.length, 0)

  return (
    <section className={styles.page}>
      <header className="page-header">
        <div>
          <h1>Guardrails</h1>
          <p>Every provider answer is inspected locally before your client sees it</p>
        </div>
        <div className={styles.headerActions}>
          <Button disabled={state.phase === 'loading'} onClick={() => void model.refresh()}>
            <RefreshCw size={15} aria-hidden="true" />Refresh
          </Button>
          <Button
            variant="danger"
            disabled={state.pending || state.findings.length === 0}
            onClick={() => void model.clear()}
          >
            <Trash2 size={15} aria-hidden="true" />Clear findings
          </Button>
        </div>
      </header>

      {state.error ? <div className={styles.error} role="alert">{state.error}</div> : null}

      <div className="page-body">
        <MetricStrip>
          {/* The recorded total, not the length of the list below: the list is bounded
              by what one page shows and what fits one protocol frame, and reading the
              metric off it would report a cap as if it were the whole journal. */}
          <Metric label="Findings" value={(status?.findingCount ?? state.findings.length).toLocaleString()} />
          <Metric label="High severity" value={counts.high.toLocaleString()} tone={counts.high > 0 ? 'warning' : undefined} />
          <Metric label="Refused answers" value={counts.blocked.toLocaleString()} tone={counts.blocked > 0 ? 'danger' : undefined} />
          <Metric label="Providers involved" value={counts.providers.size.toLocaleString()} />
          <Metric label="Rules loaded" value={(status?.ruleCount ?? 0).toLocaleString()} />
        </MetricStrip>

        <section className={styles.mode}>
          <header>
            <div><ScanEye size={17} aria-hidden="true" /><h2>Inspection mode</h2></div>
            <span>Takes effect on the next request. No restart needed.</span>
          </header>
          <div className={styles.modeChoices} role="radiogroup" aria-label="Inspection mode">
            {GUARDRAIL_MODES.map((choice) => (
              <button
                key={choice}
                type="button"
                role="radio"
                aria-checked={mode === choice}
                data-active={mode === choice || undefined}
                disabled={state.pending}
                onClick={() => void model.setMode(choice)}
              >
                <span className={styles.modeIcon} aria-hidden="true">{choice === 'off' ? <ShieldOff size={16} /> : choice === 'block' ? <ShieldAlert size={16} /> : <ScanEye size={16} />}</span>
                <strong>{modeLabel(choice)}</strong>
                <small>{modeDescription(choice)}</small>
              </button>
            ))}
          </div>
          <p className={styles.modeNote}>
            {status
              ? `${status.ruleCount.toLocaleString()} detection rules and ${status.indicatorCount.toLocaleString()} known indicators, rule set version ${status.ruleSetVersion}. The rules themselves stay inside the application.`
              : 'Loading the detection rule set…'}
          </p>
        </section>

        <section className={styles.findings}>
          <header>
            <div><ShieldAlert size={17} aria-hidden="true" /><h2>Findings</h2></div>
            {/* A shorter list than the metric is not a bug, but it must not read as the
                whole journal either: the oldest are held back so one answer's evidence
                cannot outgrow what the app can hand to this window. */}
            <span>
              {withheld > 0
                ? `Newest ${state.findings.length.toLocaleString()} of ${(status?.findingCount ?? 0).toLocaleString()}. Kept in memory only, and never sent anywhere.`
                : 'Newest first. Kept in memory only, and never sent anywhere.'}
            </span>
          </header>
          <div className={styles.tableWrap}>
            <table>
              <thead>
                <tr><th>Verdict</th><th>Severity</th><th>Provider</th><th>Model</th><th>Detected</th><th>Evidence</th><th>Time</th></tr>
              </thead>
              <tbody>
                {state.findings.map((record) => (
                  <FindingRow key={record.id} record={record} onOpen={() => setOpenId(record.id)} />
                ))}
                {state.phase !== 'loading' && state.findings.length === 0 ? (
                  <tr>
                    <td colSpan={7} className={styles.empty}>
                      <ShieldCheck size={20} aria-hidden="true" />
                      {mode === 'off' ? 'Inspection is off. Nothing is being checked.' : 'No provider has sent anything suspicious.'}
                    </td>
                  </tr>
                ) : null}
              </tbody>
            </table>
          </div>
        </section>
      </div>

      {selected ? <FindingDialog record={selected} onClose={() => setOpenId('')} /> : null}
    </section>
  )
}

function verdictView(record: Pick<GuardrailRecord, 'verdict'>) {
  if (record.verdict === 'blocked') return { label: 'Refused', tone: 'failed' as const }
  return { label: 'Delivered', tone: 'retrying' as const }
}

function FindingRow({ record, onOpen }: { record: GuardrailRecord; onOpen: () => void }) {
  const view = verdictView(record)
  const headline = record.findings[0]
  const repeats = record.occurrences ?? 1
  return (
    <tr
      tabIndex={0}
      onDoubleClick={onOpen}
      onKeyDown={(event) => {
        if (event.key !== 'Enter') return
        event.preventDefault()
        onOpen()
      }}
    >
      <td><span className={styles.state}><StatusDot state={view.tone} />{view.label}</span></td>
      <td><SeverityTag severity={record.severity} /></td>
      <td title={record.providerName}>{record.providerName || <span className={styles.muted}>—</span>}</td>
      <td title={record.model}>{record.model || <span className={styles.muted}>—</span>}</td>
      <td title={headline?.description}>
        {/* Ahead of the description, not after it: the cell ellipsises, and the one
          * finding that ever carries a count has an 84-character description that
          * fills the column on its own — a trailing badge is clipped to nothing. */}
        {repeats > 1 ? <span className={styles.repeat} title={`Seen ${repeats.toLocaleString()} times`}>×{repeats.toLocaleString()}</span> : null}
        {headline?.description || headline?.category || '—'}
      </td>
      <td className={styles.mono} title={headline?.match}>{headline?.match || '—'}</td>
      <td>
        <button type="button" className={styles.open} onClick={onOpen} aria-label={`Open the finding from ${record.providerName || 'this provider'}`}>
          {formatClock(record.at)}
        </button>
      </td>
    </tr>
  )
}

function SeverityTag({ severity }: { severity: GuardrailSeverity }) {
  return <span className={styles.severity} data-severity={severity}>{severity}</span>
}

function FindingDialog({ record, onClose }: { record: GuardrailRecord; onClose: () => void }) {
  const dialogRef = useModalFocus<HTMLElement>(onClose)
  const view = verdictView(record)
  const repeats = record.occurrences ?? 1
  return (
    <div className={styles.backdrop} role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose() }}>
      <section ref={dialogRef} className={styles.dialog} role="dialog" aria-modal="true" aria-labelledby="finding-dialog-title">
        <header>
          <div>
            <span className={styles.state}><StatusDot state={view.tone} />{view.label}</span>
            <h2 id="finding-dialog-title">{record.providerName || 'Unnamed provider'}</h2>
          </div>
          <button type="button" className={styles.close} aria-label="Close the finding" onClick={onClose}><X size={18} aria-hidden="true" /></button>
        </header>
        <div className={styles.summary}>
          <span><small>Severity</small><strong><SeverityTag severity={record.severity} /></strong></span>
          <span><small>Model</small><strong className={styles.truncate} title={record.model}>{record.model || '—'}</strong></span>
          {/* A folded row stands for many answers, so it reports how many rather than
            * leaving "Last seen" to imply a number the operator cannot read. */}
          <span><small>{repeats > 1 ? 'Answers' : 'Detections'}</small><strong>{repeats > 1 ? repeats.toLocaleString() : record.findings.length}</strong></span>
          <span><small>{repeats > 1 ? 'Last seen' : 'Time'}</small><strong>{formatClock(record.at)}</strong></span>
        </div>
        <div className={styles.detections}>
          {record.findings.map((finding, index) => (
            <Detection key={`${finding.ruleId}-${index}`} finding={finding} />
          ))}
        </div>
      </section>
    </div>
  )
}

function Detection({ finding }: { finding: GuardrailFinding }) {
  return (
    <article className={styles.detection}>
      <header>
        <SeverityTag severity={finding.severity} />
        <strong>{finding.description || finding.category}</strong>
        <small>{finding.source}</small>
      </header>
      {/* A report about the inspection itself has nothing matched to show, and an
        * empty code block reads as evidence that failed to load. */}
      {finding.match ? <code>{finding.match}</code> : null}
      {finding.excerpt ? <p>{finding.excerpt}</p> : null}
    </article>
  )
}
