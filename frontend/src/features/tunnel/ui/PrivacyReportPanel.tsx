import { RefreshCw, ShieldCheck, TriangleAlert } from 'lucide-react'
import { Button } from '../../../shared/ui/Button'
import { privacyWarnings, type PrivacyReport } from '../domain/privacy'
import styles from './TunnelPage.module.css'

export function PrivacyReportPanel({ report, pending, error, disabled, onRun }: {
  readonly report: PrivacyReport | null
  readonly pending: boolean
  readonly error: string
  readonly disabled: boolean
  readonly onRun: () => void
}) {
  const warnings = report ? privacyWarnings(report) : []
  const clean = Boolean(report && report.status === 200 && warnings.length === 0)

  return <section className={styles.privacy} aria-labelledby="privacy-title">
    <header className={styles.privacyHeader}>
      <div className={styles.privacyIntro}><ShieldCheck size={20} /><div><h2 id="privacy-title">Public privacy test</h2><p>See exactly what an authenticated external client receives from <code>/v1/models</code>. The Go sidecar uses the access key without returning it to the UI.</p></div></div>
      <Button type="button" disabled={disabled || pending} onClick={onRun}>{pending ? <RefreshCw className={styles.spin} size={14} /> : <ShieldCheck size={14} />}{pending ? 'Testing…' : 'Run privacy test'}</Button>
    </header>
    {disabled && !report ? <p className={styles.privacyHint}>Start the tunnel before running the external check.</p> : null}
    {error ? <div className={styles.privacyError} role="alert">{error}</div> : null}
    {report ? <div className={styles.privacyResult} aria-live="polite">
      <div className={styles.verdict} data-risk={clean ? 'clean' : 'review'}>{clean ? <ShieldCheck size={16} /> : <TriangleAlert size={16} />}<div><strong>{clean ? 'No direct provider metadata detected' : 'Review exposed metadata'}</strong><span>{clean ? 'Public model aliases remain visible by design.' : `${warnings.length} signal${warnings.length === 1 ? '' : 's'} need attention.`}</span></div></div>
      {warnings.length > 0 ? <ul className={styles.warningList}>{warnings.map((warning) => <li key={warning}>{warning}</li>)}</ul> : null}
      <dl className={styles.metrics}>
        <Metric label="Status" value={report.statusText || String(report.status)} />
        <Metric label="Protocol" value={report.protocol || 'Unknown'} />
        <Metric label="Latency" value={`${report.durationMs} ms`} />
        <Metric label="Body" value={formatBytes(report.bodyBytes)} />
        <Metric label="Headers" value={String(report.headers.length)} />
        <Metric label="Models" value={String(report.models.length)} />
      </dl>
      <section className={styles.connection}>
        <h3>Connection</h3>
        <dl>
          <Detail label="Request" value={report.requestUrl} />
          <Detail label="Checked" value={formatDate(report.checkedAt)} />
          <Detail label="Remote" value={report.remoteAddress || 'Unavailable'} />
          <Detail label="TLS" value={report.tls ? `${report.tls.version} · ${report.tls.cipherSuite}` : 'Plain loopback HTTP'} />
          {report.tls ? <><Detail label="SNI" value={report.tls.serverName || 'Unavailable'} /><Detail label="Certificate" value={report.tls.certificateSubject || 'Unavailable'} /><Detail label="Issuer" value={report.tls.certificateIssuer || 'Unavailable'} /><Detail label="Expires" value={formatDate(report.tls.certificateExpiresAt)} /></> : null}
        </dl>
      </section>
      <div className={styles.reportGrid}>
        <section className={styles.reportSection}><header><h3>Response headers</h3><span>{report.headers.length}</span></header><div className={styles.dataRows}>{report.headers.map((header) => <div className={styles.dataRow} key={header.name}><code>{header.name}</code><span>{header.values.join('\n') || 'Empty value'}</span></div>)}</div></section>
        <section className={styles.reportSection}><header><h3>Public models</h3><span>{report.models.length}</span></header><div className={styles.dataRows}>{report.models.length > 0 ? report.models.map((model, index) => <div className={styles.modelRow} key={`${model.id}:${index}`}><strong>{model.id || 'Empty id'}</strong><span>{model.object || 'Unknown object'} · created {model.created}</span><code>{model.fields.join(', ')}</code></div>) : <p className={styles.empty}>No model records returned.</p>}</div></section>
      </div>
      <details className={styles.raw}><summary>Raw response and JSON fields</summary><div><span>Top-level fields: <code>{report.topLevelFields.join(', ') || 'none'}</code></span><pre>{report.rawBody || 'Empty response body'}</pre></div></details>
    </div> : null}
  </section>
}

function Metric({ label, value }: { readonly label: string; readonly value: string }) {
  return <div><dt>{label}</dt><dd>{value}</dd></div>
}

function Detail({ label, value }: { readonly label: string; readonly value: string }) {
  return <div><dt>{label}</dt><dd>{value}</dd></div>
}

function formatBytes(value: number): string {
  if (value < 1024) return `${value} B`
  return `${(value / 1024).toFixed(1)} KiB`
}

function formatDate(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}
