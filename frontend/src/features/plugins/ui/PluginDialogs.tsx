import { useId, useMemo, useState } from 'react'
import { AlertTriangle, ExternalLink, X } from 'lucide-react'
import { formatBytes } from '../../../shared/format/metrics'
import { Pill, type PillTone } from '../../../shared/ui/chrome'
import { Button } from '../../../shared/ui/Button'
import { useModalFocus } from '../../../shared/ui/useModalFocus'
import { openExternal } from '../../../platform/lifecycle/open-external'
import { byteLength, needsNewerApp, packageBytes, packageSizeProblem, parsePackage, type PluginPackage } from '../domain/manifest'
import { DANGEROUS_PERMISSIONS, initialGrants, isPluginPermission, reviewedBefore, permissionSpec, type PermissionRisk, type PluginPermission } from '../domain/permissions'
import type { PluginSummary } from '../domain/plugin'
import { useStyleHold } from './useStyleHold'
import styles from './PluginsPage.module.css'

const RISK: Readonly<Record<PermissionRisk, { readonly label: string; readonly tone: PillTone }>> = {
  low: { label: 'Low impact', tone: 'neutral' },
  medium: { label: 'Medium impact', tone: 'warning' },
  high: { label: 'High impact', tone: 'danger' },
}

export function RiskPill({ risk }: { readonly risk: PermissionRisk }) {
  return <Pill tone={RISK[risk].tone}>{RISK[risk].label}</Pill>
}

/** Paste a package's JSON; problems are listed under the field. */
export function PasteDialog({ onCancel, onReview }: {
  readonly onCancel: () => void
  readonly onReview: (pkg: PluginPackage) => void
}) {
  const ref = useModalFocus<HTMLElement>(onCancel)
  const [text, setText] = useState('')
  const [problems, setProblems] = useState<readonly string[]>([])
  const field = useId()
  const review = () => {
    const checked = parsePackage(text)
    if (checked.ok) onReview(checked.package)
    else setProblems(checked.problems)
  }
  return (
    <div className="ui-scrim">
      <section ref={ref} className={`ui-modal ${styles.dialog}`} role="dialog" aria-modal="true" aria-labelledby={`${field}-title`}>
        <header>
          <div>
            <h2 id={`${field}-title`}>Paste a plugin</h2>
            <p>Paste the whole package file. You review its permissions before anything is installed.</p>
          </div>
          <button type="button" aria-label="Close" onClick={onCancel}><X size={16} /></button>
        </header>
        <div className={styles.dialogBody}>
          <label className={styles.fieldLabel} htmlFor={field}>Package JSON</label>
          <textarea
            id={field}
            className={styles.pasteArea}
            value={text}
            spellCheck={false}
            data-autofocus
            aria-invalid={problems.length > 0 || undefined}
            aria-describedby={problems.length ? `${field}-problems` : undefined}
            placeholder={'{\n  "schema": 1,\n  "id": "acme.tools",\n  "name": "Acme tools",\n  "version": "1.0.0",\n  "permissions": ["ui:pages"],\n  "script": "…"\n}'}
            onChange={(event) => { setText(event.target.value); setProblems([]) }}
          />
          <small className={styles.fieldHint}>{formatBytes(byteLength(text))}</small>
          {problems.length ? <Problems id={`${field}-problems`} problems={problems} /> : null}
        </div>
        <footer>
          <Button onClick={onCancel}>Cancel</Button>
          <Button variant="primary" disabled={!text.trim()} onClick={review}>Review</Button>
        </footer>
      </section>
    </div>
  )
}

/** Loads a plugin from a folder on this computer, for development: the
 * folder's `plugin.json` is read again whenever it changes. */
export function DevLinkDialog({ pending, error, onCancel, onLink }: {
  readonly pending: boolean
  readonly error: string
  readonly onCancel: () => void
  readonly onLink: (path: string) => void
}) {
  const ref = useModalFocus<HTMLElement>(onCancel)
  const [path, setPath] = useState('')
  const field = useId()
  return (
    <div className="ui-scrim">
      <section ref={ref} className={`ui-modal ${styles.dialog}`} role="dialog" aria-modal="true" aria-labelledby={`${field}-title`}>
        <header>
          <div>
            <h2 id={`${field}-title`}>Load a plugin from a folder</h2>
            <p>For plugin development. The plugin reloads when its files change. New permissions start ungranted; grant them under Permissions.</p>
          </div>
          <button type="button" aria-label="Close" onClick={onCancel}><X size={16} /></button>
        </header>
        <form className={styles.dialogBody} onSubmit={(event) => { event.preventDefault(); if (path.trim() && !pending) onLink(path) }}>
          <label className={styles.fieldLabel} htmlFor={field}>Folder with plugin.json</label>
          <input
            id={field}
            value={path}
            spellCheck={false}
            autoComplete="off"
            data-autofocus
            placeholder="C:\Users\you\my-plugin"
            aria-invalid={error ? true : undefined}
            aria-describedby={error ? `${field}-error` : undefined}
            onChange={(event) => setPath(event.target.value)}
          />
          <small className={styles.fieldHint}>An absolute path. Only plugins you are writing belong here.</small>
          {error ? <p id={`${field}-error`} className={styles.dialogError} role="alert">{error}</p> : null}
          <button type="submit" hidden />
        </form>
        <footer>
          <Button onClick={onCancel}>Cancel</Button>
          <Button variant="primary" disabled={!path.trim() || pending} onClick={() => onLink(path)}>{pending ? 'Loading…' : 'Load'}</Button>
        </footer>
      </section>
    </div>
  )
}

/** A package file that could not be read. */
export function ProblemsDialog({ title, problems, onClose }: { readonly title: string; readonly problems: readonly string[]; readonly onClose: () => void }) {
  const ref = useModalFocus<HTMLElement>(onClose)
  const id = useId()
  return (
    <div className="ui-scrim">
      <section ref={ref} className={`ui-modal ${styles.dialog}`} role="alertdialog" aria-modal="true" aria-labelledby={`${id}-title`}>
        <header>
          <div>
            <h2 id={`${id}-title`}>{title}</h2>
            <p>Nothing was installed. Fix the package and try again.</p>
          </div>
        </header>
        <div className={styles.dialogBody}><Problems problems={problems} /></div>
        <footer><Button variant="primary" data-autofocus onClick={onClose}>Close</Button></footer>
      </section>
    </div>
  )
}

function Problems({ id, problems }: { readonly id?: string; readonly problems: readonly string[] }) {
  return (
    <div id={id} className={styles.problems} role="alert">
      <strong><AlertTriangle size={14} aria-hidden="true" />{problems.length === 1 ? 'One problem' : `${problems.length} problems`}</strong>
      <ul>{problems.slice(0, 20).map((problem, index) => <li key={index}>{problem}</li>)}</ul>
      {problems.length > 20 ? <p>…and {problems.length - 20} more.</p> : null}
    </div>
  )
}

/**
 * The consent step. It shows who the plugin says it is, what it contains and
 * each permission with what granting it means; the operator ticks what the
 * plugin may do. Nothing is installed until Install is pressed.
 */
export function InstallReviewDialog({ pkg, existing, appVersion = '', pending, error, onCancel, onInstall }: {
  readonly pkg: PluginPackage
  readonly existing: PluginSummary | undefined
  /** The running app's version; a package that needs a newer one is refused. */
  readonly appVersion?: string
  readonly pending: boolean
  readonly error: string
  readonly onCancel: () => void
  readonly onInstall: (granted: readonly PluginPermission[]) => void
}) {
  const ref = useModalFocus<HTMLElement>(onCancel, pending)
  const id = useId()
  const [granted, setGranted] = useState<ReadonlySet<PluginPermission>>(() => new Set(initialGrants(pkg.permissions, existing, pkg.network?.hosts)))
  const ordered = pkg.permissions.filter((permission) => granted.has(permission))
  const sizeProblem = useMemo(() => packageSizeProblem(pkg, ordered), [pkg, ordered])
  const tooNew = needsNewerApp(pkg.minAppVersion, appVersion)
  const previous = existing ? reviewedBefore(existing, pkg.network?.hosts ?? []) : new Set<string>()
  useStyleHold()
  const upgrade = existing !== undefined
  const contents = [
    pkg.theme ? `Theme “${pkg.theme.name}” (${Object.keys(pkg.theme.tokens).length} tokens)` : '',
    pkg.settings?.length ? `${pkg.settings.length} ${pkg.settings.length === 1 ? 'setting' : 'settings'} you can change` : '',
    pkg.css ? `Stylesheet (${formatBytes(byteLength(pkg.css))})` : '',
    pkg.script ? `Script (${formatBytes(byteLength(pkg.script))}), runs isolated from the window` : '',
  ].filter(Boolean)
  const toggle = (permission: PluginPermission, on: boolean) => {
    const next = new Set(granted)
    if (on) next.add(permission)
    else next.delete(permission)
    setGranted(next)
  }
  return (
    <div className="ui-scrim">
      <section ref={ref} className={`ui-modal ${styles.dialog} ${styles.reviewDialog}`} role="dialog" aria-modal="true" aria-labelledby={`${id}-title`} aria-describedby={`${id}-summary`}>
        <header>
          <div>
            <h2 id={`${id}-title`}>{upgrade ? `Update ${pkg.name}?` : `Install ${pkg.name}?`}</h2>
            <p id={`${id}-summary`}>
              {upgrade
                ? `${existing.version} → ${pkg.version}. Its stored data is kept and it is turned on.`
                : `Version ${pkg.version}${pkg.author ? ` by ${pkg.author}` : ''}. Only install plugins from people you trust.`}
            </p>
          </div>
          <button type="button" aria-label="Close" disabled={pending} onClick={onCancel}><X size={16} /></button>
        </header>
        <div className={styles.dialogBody}>
          {pkg.description ? <p className={styles.reviewDescription}>{pkg.description}</p> : null}
          <dl className={styles.facts}>
            <div><dt>Identifier</dt><dd><code>{pkg.id}</code></dd></div>
            {pkg.author ? <div><dt>Author</dt><dd>{pkg.author}</dd></div> : null}
            {pkg.homepage ? (
              <div>
                <dt>Homepage</dt>
                <dd>
                  <button type="button" className={styles.homepage} disabled={pending} onClick={() => void openExternal(pkg.homepage!)} title="Opens in your browser">
                    <span>{pkg.homepage}</span><ExternalLink size={12} aria-hidden="true" />
                  </button>
                </dd>
              </div>
            ) : null}
            {pkg.minAppVersion ? <div><dt>Needs app</dt><dd>{pkg.minAppVersion} or later{appVersion ? ` (this is ${appVersion})` : ''}</dd></div> : null}
            {pkg.network?.hosts.length ? <div><dt>Network</dt><dd>{pkg.network.hosts.join(', ')}</dd></div> : null}
            <div><dt>Contains</dt><dd>{contents.length ? contents.join(' · ') : 'Nothing but its name'}</dd></div>
            <div><dt>Size</dt><dd>{formatBytes(packageBytes(pkg))}</dd></div>
          </dl>
          <fieldset className={styles.grants}>
            <legend>Permissions</legend>
            {pkg.permissions.length === 0 ? <p className={styles.muted}>This plugin asks for no permissions.</p> : null}
            {pkg.permissions.map((permission) => {
              const spec = permissionSpec(permission)
              const checkbox = `${id}-${permission}`
              return (
                <div key={permission} className={styles.grant} data-risk={spec.risk}>
                  <input id={checkbox} type="checkbox" checked={granted.has(permission)} disabled={pending} onChange={(event) => toggle(permission, event.target.checked)} />
                  <label htmlFor={checkbox}>
                    <span className={styles.grantTitle}>
                      <strong>{spec.title}</strong>
                      <code>{permission}</code>
                      <RiskPill risk={spec.risk} />
                      {upgrade && !previous.has(permission) ? <Pill tone="info">New</Pill> : null}
                    </span>
                    <span className={styles.grantText}>{spec.description}</span>
                  </label>
                </div>
              )
            })}
          </fieldset>
          {pkg.permissions.length > 0 && ordered.length < pkg.permissions.length ? (
            <p className={styles.muted}>Calls that need an unticked permission fail inside the plugin; it keeps running. You can change this later.</p>
          ) : null}
          {ordered.some((permission) => DANGEROUS_PERMISSIONS.has(permission)) ? (
            <p className={styles.dialogError} role="alert">
              You ticked {ordered.filter((permission) => DANGEROUS_PERMISSIONS.has(permission)).map((permission) => permissionSpec(permission).title).join(', ')}. These let the plugin reach credentials, the internet or your prompts. Grant them only to plugins you wrote or fully trust.
            </p>
          ) : null}
          {tooNew ? <p className={styles.dialogError} role="alert">This plugin needs Luxury Switchboard {pkg.minAppVersion} or later; this is {appVersion}. Update the app first.</p> : null}
          {sizeProblem ? <p className={styles.dialogError} role="alert">{sizeProblem}</p> : null}
          {error ? <p className={styles.dialogError} role="alert">{error}</p> : null}
        </div>
        <footer>
          <Button disabled={pending} onClick={onCancel}>Cancel</Button>
          <Button variant="primary" data-autofocus disabled={pending || sizeProblem !== '' || tooNew} aria-busy={pending || undefined} onClick={() => onInstall(ordered)}>
            {pending ? (upgrade ? 'Updating…' : 'Installing…') : upgrade ? 'Update' : 'Install'}
          </Button>
        </footer>
      </section>
    </div>
  )
}

/** Changes what an installed plugin may do; it restarts with the new set. */
export function PermissionsDialog({ plugin, pending, error, onCancel, onSave }: {
  readonly plugin: PluginSummary
  readonly pending: boolean
  readonly error: string
  readonly onCancel: () => void
  readonly onSave: (granted: readonly PluginPermission[]) => void
}) {
  const ref = useModalFocus<HTMLElement>(onCancel, pending)
  const id = useId()
  const declared = plugin.permissions.filter(isPluginPermission)
  const [granted, setGranted] = useState<ReadonlySet<string>>(() => new Set(plugin.granted))
  const ordered = declared.filter((permission) => granted.has(permission))
  const changed = ordered.length !== plugin.granted.length || ordered.some((permission) => !plugin.granted.includes(permission))
  useStyleHold()
  return (
    <div className="ui-scrim">
      <section ref={ref} className={`ui-modal ${styles.dialog} ${styles.reviewDialog}`} role="dialog" aria-modal="true" aria-labelledby={`${id}-title`}>
        <header>
          <div>
            <h2 id={`${id}-title`}>Permissions for {plugin.name}</h2>
            <p>{plugin.hasScript ? 'Saving restarts the plugin with the new permissions.' : 'Saving applies the change right away.'}</p>
          </div>
          <button type="button" aria-label="Close" disabled={pending} onClick={onCancel}><X size={16} /></button>
        </header>
        <div className={styles.dialogBody}>
          <fieldset className={styles.grants}>
            <legend className="sr-only">Granted permissions</legend>
            {declared.length === 0 ? <p className={styles.muted}>This plugin asks for no permissions.</p> : null}
            {declared.map((permission) => {
              const spec = permissionSpec(permission)
              const checkbox = `${id}-${permission}`
              return (
                <div key={permission} className={styles.grant} data-risk={spec.risk}>
                  <input
                    id={checkbox}
                    type="checkbox"
                    checked={granted.has(permission)}
                    disabled={pending}
                    data-autofocus={permission === declared[0] || undefined}
                    onChange={(event) => {
                      const next = new Set(granted)
                      if (event.target.checked) next.add(permission)
                      else next.delete(permission)
                      setGranted(next)
                    }}
                  />
                  <label htmlFor={checkbox}>
                    <span className={styles.grantTitle}>
                      <strong>{spec.title}</strong>
                      <code>{permission}</code>
                      <RiskPill risk={spec.risk} />
                    </span>
                    <span className={styles.grantText}>{spec.description}</span>
                  </label>
                </div>
              )
            })}
          </fieldset>
          {error ? <p className={styles.dialogError} role="alert">{error}</p> : null}
        </div>
        <footer>
          <Button disabled={pending} onClick={onCancel}>Cancel</Button>
          <Button variant="primary" disabled={pending || !changed} aria-busy={pending || undefined} onClick={() => onSave(ordered)}>
            {pending ? 'Saving…' : 'Save'}
          </Button>
        </footer>
      </section>
    </div>
  )
}

export function RemoveDialog({ plugin, pending, error, onCancel, onConfirm }: {
  readonly plugin: PluginSummary
  readonly pending: boolean
  readonly error: string
  readonly onCancel: () => void
  readonly onConfirm: () => void
}) {
  const ref = useModalFocus<HTMLElement>(onCancel, pending)
  const id = useId()
  useStyleHold()
  return (
    <div className="ui-scrim">
      <section ref={ref} className={`ui-modal ${styles.dialog}`} role="alertdialog" aria-modal="true" aria-labelledby={`${id}-title`} aria-describedby={`${id}-body`}>
        <header>
          <div>
            <h2 id={`${id}-title`}>Remove {plugin.name}?</h2>
            <p id={`${id}-body`}>Its pages, commands, status readouts, theme and stylesheet leave the window, and the data it stored on this machine is deleted.</p>
          </div>
        </header>
        {error ? <div className={styles.dialogBody}><p className={styles.dialogError} role="alert">{error}</p></div> : null}
        <footer>
          <Button disabled={pending} data-autofocus onClick={onCancel}>Cancel</Button>
          <Button variant="danger" disabled={pending} aria-busy={pending || undefined} onClick={onConfirm}>{pending ? 'Removing…' : 'Remove'}</Button>
        </footer>
      </section>
    </div>
  )
}
