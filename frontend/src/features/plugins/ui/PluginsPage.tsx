import { useRef, useState, useSyncExternalStore, type ChangeEvent } from 'react'
import { ChevronDown, ClipboardPaste, Download, ExternalLink, FolderOpen, LayoutGrid, LifeBuoy, Link2, Link2Off, Puzzle, RefreshCw, RotateCcw, ShieldCheck, SlidersHorizontal, Trash2, TriangleAlert, Upload, X } from 'lucide-react'
import { EmptyState, Panel, Pill, type PillTone } from '../../../shared/ui/chrome'
import { Button } from '../../../shared/ui/Button'
import { formatBytes } from '../../../shared/format/metrics'
import { restartInSafeMode } from '../../../shared/runtime/safe-mode'
import { openExternal } from '../../../platform/lifecycle/open-external'
import { useAppServices } from '../../../app/services'
import { EXAMPLE_PLUGINS } from '../domain/examples'
import { PLUGIN_LIMIT, parsePackage, type PluginPackage } from '../domain/manifest'
import { isPluginPermission, permissionSpec } from '../domain/permissions'
import { knownTime, type LogEntry, type PluginRuntime, type PluginSummary, type RuntimeState } from '../domain/plugin'
import { DevLinkDialog, InstallReviewDialog, LinkDialog, PasteDialog, PermissionsDialog, ProblemsDialog, RemoveDialog } from './PluginDialogs'
import { PluginReference } from './PluginReference'
import { PluginSettingsDialog } from './PluginSettingsDialog'
import { usePlugins } from './usePlugins'
import styles from './PluginsPage.module.css'

type Dialog =
  | { readonly kind: 'paste' }
  | { readonly kind: 'dev'; readonly error: string }
  | { readonly kind: 'link'; readonly error: string; readonly problems: readonly string[]; readonly url?: string }
  | { readonly kind: 'review'; readonly pkg: PluginPackage; readonly error: string; readonly source?: string }
  | { readonly kind: 'problems'; readonly title: string; readonly problems: readonly string[] }
  | { readonly kind: 'permissions'; readonly id: string }
  | { readonly kind: 'settings'; readonly id: string }
  | { readonly kind: 'remove'; readonly id: string }

/** Files larger than this cannot be a valid package (one command frame). */
const FILE_LIMIT = 512 * 1024

export default function PluginsPage() {
  const { model, state } = usePlugins()
  const { appVersion, capabilities, extensions } = useAppServices()
  const layout = useSyncExternalStore(extensions.subscribe, extensions.snapshot).layout
  const [dialog, setDialog] = useState<Dialog | null>(null)
  const [notice, setNotice] = useState('')
  const [expanded, setExpanded] = useState<string>('')
  const [saving, setSaving] = useState('')
  const fileInput = useRef<HTMLInputElement>(null)
  const busy = state.pending !== ''
  const full = state.plugins.length >= PLUGIN_LIMIT

  const review = (pkg: PluginPackage, source?: string) => {
    model.clearError()
    setNotice('')
    setDialog({ kind: 'review', pkg, error: '', source })
  }

  const pick = (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0]
    event.target.value = ''
    if (!file) return
    if (file.size > FILE_LIMIT) {
      setDialog({ kind: 'problems', title: `${file.name} cannot be installed`, problems: [`The file is ${formatBytes(file.size)}; a plugin package is at most ${FILE_LIMIT / 1024} KiB.`] })
      return
    }
    void file.text().then((text) => {
      const checked = parsePackage(text)
      if (checked.ok) review(checked.package)
      else setDialog({ kind: 'problems', title: `${file.name} is not a plugin package`, problems: checked.problems })
    }, () => setDialog({ kind: 'problems', title: 'The file could not be read', problems: ['The file could not be read from disk.'] }))
  }

  const download = async (url: string) => {
    const result = await model.download(url)
    if (result.ok) review(result.pkg, result.source)
    else setDialog({ kind: 'link', error: result.message, problems: result.problems ?? [] })
  }

  const install = async (pkg: PluginPackage, granted: readonly string[], source?: string) => {
    const upgrade = state.plugins.some((plugin) => plugin.id === pkg.id)
    const result = await model.install(pkg, granted, source)
    if (result.ok) {
      setDialog(null)
      setNotice(`${result.plugin.name} ${result.plugin.version} ${upgrade ? 'updated' : 'installed'}.`)
    } else {
      setDialog({ kind: 'review', pkg, error: result.message, source })
    }
  }

  const savePackage = async (plugin: PluginSummary) => {
    setNotice('')
    setSaving(plugin.id)
    const saved = await model.savePackage(plugin.id)
    setSaving('')
    if (saved) setNotice(`${plugin.name} ${plugin.version} saved as a package file. Settings values and stored data are not included.`)
  }

  const link = async (path: string) => {
    const result = await model.devLink(path)
    if (result.ok) {
      setDialog(null)
      setNotice(`${result.plugin.name} ${result.plugin.version} loaded from its folder.`)
    } else {
      setDialog({ kind: 'dev', error: result.message })
    }
  }

  const current = dialog && (dialog.kind === 'permissions' || dialog.kind === 'settings' || dialog.kind === 'remove')
    ? state.plugins.find((plugin) => plugin.id === dialog.id)
    : undefined

  return (
    <section className={styles.page}>
      <header className="page-header">
        <div>
          <h1>Plugins</h1>
          <p>Themes, stylesheets, pages, commands and automations from packages you install. Each runs isolated, with only the permissions you grant.</p>
        </div>
        <div className={styles.headerActions}>
          <Button onClick={() => fileInput.current?.click()} disabled={busy || state.phase !== 'ready'}>
            <Upload size={14} aria-hidden="true" />Install from file
          </Button>
          {capabilities.includes('plugins.download') ? (
            <Button onClick={() => { model.clearError(); setNotice(''); setDialog({ kind: 'link', error: '', problems: [] }) }} disabled={busy || state.phase !== 'ready'}>
              <Link2 size={14} aria-hidden="true" />Install from link…
            </Button>
          ) : null}
          <Button onClick={() => { setNotice(''); setDialog({ kind: 'paste' }) }} disabled={busy || state.phase !== 'ready'}>
            <ClipboardPaste size={14} aria-hidden="true" />Paste JSON
          </Button>
          <Button onClick={() => { model.clearError(); setNotice(''); setDialog({ kind: 'dev', error: '' }) }} disabled={busy || state.phase !== 'ready'}>
            <FolderOpen size={14} aria-hidden="true" />Load from folder…
          </Button>
          <input ref={fileInput} type="file" accept=".json,application/json" className="sr-only" aria-label="Plugin package file" tabIndex={-1} onChange={pick} />
        </div>
      </header>

      {state.safeMode ? (
        <div className={styles.banner} data-tone="warning" role="status">
          <LifeBuoy size={14} aria-hidden="true" />
          <span>Safe mode is on: no plugin code, stylesheet or theme is running. You can still turn plugins off, change permissions or remove them.</span>
          <button type="button" className={styles.linkButton} onClick={() => restartInSafeMode(false)}>Leave safe mode</button>
        </div>
      ) : null}
      {layout ? (
        <div className={styles.banner} data-tone="warning" role="status">
          <LayoutGrid size={14} aria-hidden="true" />
          <span>Layout customised by {layout.owner}: it may hide sidebar items or choose the start page.</span>
          <button type="button" className={styles.linkButton} onClick={() => layout.reset()}>Reset layout</button>
        </div>
      ) : null}
      {state.error && !dialog ? (
        <div className={styles.banner} data-tone="danger" role="alert">
          <span>{state.error}</span>
          <button type="button" className={styles.iconButton} aria-label="Dismiss" onClick={() => model.clearError()}><X size={14} /></button>
        </div>
      ) : null}

      <div className="page-body">
        {notice ? <p className={styles.notice} role="status">{notice}</p> : null}

        <Panel
          title="Installed"
          subtitle={state.phase === 'ready' ? `${state.plugins.length} of ${PLUGIN_LIMIT}` : undefined}
          actions={state.phase === 'error' ? <Button onClick={() => void model.refresh()}><RotateCcw size={13} aria-hidden="true" />Retry</Button> : undefined}
        >
          {state.phase === 'loading' ? <p className={styles.placeholder}>Loading plugins…</p> : null}
          {state.phase === 'error' && state.plugins.length === 0 ? <EmptyState icon={Puzzle} title="Plugins are unavailable" hint="The control plane did not answer. Retry in a moment." /> : null}
          {state.phase === 'ready' && state.plugins.length === 0 ? (
            <EmptyState icon={Puzzle} title="No plugins yet" hint="Install a package file, paste one, or try an example below." />
          ) : null}
          {state.plugins.length > 0 ? (
            <ul className={styles.plugins}>
              {state.plugins.map((plugin) => (
                <PluginRow
                  key={plugin.id}
                  plugin={plugin}
                  runtime={state.runtimes[plugin.id]}
                  pending={state.pending === plugin.id}
                  busy={busy}
                  safeMode={state.safeMode}
                  expanded={expanded === plugin.id}
                  onExpand={() => setExpanded(expanded === plugin.id ? '' : plugin.id)}
                  onToggle={() => { setNotice(''); void model.setEnabled(plugin.id, !plugin.enabled) }}
                  onPermissions={() => { model.clearError(); setDialog({ kind: 'permissions', id: plugin.id }) }}
                  onSettings={() => { model.clearError(); setNotice(''); setDialog({ kind: 'settings', id: plugin.id }) }}
                  onRestart={() => void model.restart(plugin.id)}
                  onRemove={() => { model.clearError(); setDialog({ kind: 'remove', id: plugin.id }) }}
                  onUpdate={plugin.source && !plugin.dev && capabilities.includes('plugins.download')
                    ? () => { model.clearError(); setNotice(''); setDialog({ kind: 'link', error: '', problems: [], url: plugin.source }) }
                    : undefined}
                  saving={saving === plugin.id}
                  onSave={capabilities.includes('plugins.export') ? () => void savePackage(plugin) : undefined}
                  onUnlink={() => { setNotice(''); void model.devUnlink(plugin.id).then((ok) => { if (ok) setNotice(`${plugin.name} is no longer loaded from its folder; the last loaded version stays installed.`) }) }}
                />
              ))}
            </ul>
          ) : null}
        </Panel>

        <Panel title="Examples" subtitle="Ready-made packages. Installing one opens the same review as any other.">
          <ul className={styles.examples}>
            {EXAMPLE_PLUGINS.map((example) => {
              const installed = state.plugins.find((plugin) => plugin.id === example.package.id)
              return (
                <li key={example.file}>
                  <div>
                    <strong>{example.package.name}</strong>
                    <p>{example.summary}</p>
                    <span className={styles.chips}>
                      {example.package.permissions.map((permission) => <code key={permission}>{permission}</code>)}
                    </span>
                  </div>
                  <Button onClick={() => review(example.package)} disabled={busy || state.phase !== 'ready' || (!installed && full)}>
                    {installed ? (installed.version === example.package.version ? 'Reinstall' : 'Update') : 'Install'}
                  </Button>
                </li>
              )
            })}
          </ul>
        </Panel>

        <Panel title="Developer reference" subtitle="Everything a package can contain and do.">
          <PluginReference />
        </Panel>
      </div>

      {dialog?.kind === 'dev' ? <DevLinkDialog pending={state.pending === 'install'} error={dialog.error} onCancel={() => setDialog(null)} onLink={(path) => void link(path)} /> : null}
      {dialog?.kind === 'link' ? <LinkDialog pending={state.pending === 'install'} error={dialog.error} problems={dialog.problems} initialUrl={dialog.url} onCancel={() => setDialog(null)} onDownload={(url) => void download(url)} /> : null}
      {dialog?.kind === 'paste' ? <PasteDialog onCancel={() => setDialog(null)} onReview={review} /> : null}
      {dialog?.kind === 'problems' ? <ProblemsDialog title={dialog.title} problems={dialog.problems} onClose={() => setDialog(null)} /> : null}
      {dialog?.kind === 'review' ? (
        <InstallReviewDialog
          pkg={dialog.pkg}
          existing={state.plugins.find((plugin) => plugin.id === dialog.pkg.id)}
          appVersion={appVersion}
          source={dialog.source}
          pending={state.pending === 'install'}
          error={dialog.error || (!state.plugins.some((plugin) => plugin.id === dialog.pkg.id) && full ? `At most ${PLUGIN_LIMIT} plugins can be installed; remove one first.` : '')}
          onCancel={() => setDialog(null)}
          onInstall={(granted) => void install(dialog.pkg, granted, dialog.source)}
        />
      ) : null}
      {dialog?.kind === 'permissions' && current ? (
        <PermissionsDialog
          plugin={current}
          pending={state.pending === current.id}
          error={state.error}
          onCancel={() => { model.clearError(); setDialog(null) }}
          onSave={(granted) => void model.setGrants(current.id, granted).then((ok) => {
            if (!ok) return
            setDialog(null)
            setNotice(`Permissions for ${current.name} saved.`)
          })}
        />
      ) : null}
      {dialog?.kind === 'settings' && current ? (
        <PluginSettingsDialog
          plugin={current}
          model={model}
          pending={state.pending === current.id}
          onClose={() => setDialog(null)}
          onSaved={() => {
            setDialog(null)
            setNotice(`Settings for ${current.name} saved.`)
          }}
        />
      ) : null}
      {dialog?.kind === 'remove' && current ? (
        <RemoveDialog
          plugin={current}
          pending={state.pending === current.id}
          error={state.error}
          onCancel={() => { model.clearError(); setDialog(null) }}
          onConfirm={() => void model.remove(current.id).then((ok) => {
            if (!ok) return
            setDialog(null)
            setNotice(`${current.name} removed.`)
          })}
        />
      ) : null}
    </section>
  )
}

const STATE: Readonly<Record<RuntimeState, { readonly label: string; readonly tone: PillTone }>> = {
  disabled: { label: 'Off', tone: 'neutral' },
  active: { label: 'Active', tone: 'success' },
  starting: { label: 'Starting', tone: 'info' },
  running: { label: 'Running', tone: 'success' },
  crashed: { label: 'Stopped', tone: 'danger' },
  stopped: { label: 'Not running', tone: 'neutral' },
}

function PluginRow({ plugin, runtime, pending, busy, safeMode, expanded, saving, onExpand, onToggle, onPermissions, onSettings, onRestart, onRemove, onUnlink, onUpdate, onSave }: {
  readonly plugin: PluginSummary
  readonly runtime: PluginRuntime | undefined
  readonly pending: boolean
  readonly busy: boolean
  readonly safeMode: boolean
  readonly expanded: boolean
  readonly onExpand: () => void
  readonly onToggle: () => void
  readonly onPermissions: () => void
  readonly onSettings: () => void
  readonly onRestart: () => void
  readonly onRemove: () => void
  readonly onUnlink: () => void
  readonly saving: boolean
  /** Reinstalls from the link the plugin came from; absent without one. */
  readonly onUpdate?: () => void
  /** Saves the installed package as a file; absent when unsupported. */
  readonly onSave?: () => void
}) {
  // A stored plugin this version cannot use is off whatever its switch says;
  // turning it on is refused until an update or a newer app fixes it.
  const blocked = plugin.problem !== ''
  const state = blocked ? 'disabled' : runtime?.state ?? (plugin.enabled ? 'starting' : 'disabled')
  const display = blocked ? { label: 'Unavailable', tone: 'warning' as const } : STATE[state]
  const declared = plugin.permissions.filter(isPluginPermission)
  const granted = new Set(plugin.granted)
  const contents = [plugin.hasTheme ? 'theme' : '', plugin.hasCss ? 'stylesheet' : '', plugin.hasScript ? 'script' : ''].filter(Boolean)
  const canRestart = !blocked && plugin.enabled && plugin.hasScript && !safeMode && (state === 'crashed' || state === 'running' || state === 'stopped')
  const details = `${plugin.id}-details`
  return (
    <li className={styles.plugin} data-enabled={(plugin.enabled && !blocked) || undefined} aria-busy={pending || undefined}>
      <div className={styles.pluginMain}>
        <button
          type="button"
          role="switch"
          className={styles.switch}
          aria-checked={plugin.enabled && !blocked}
          aria-label={`${plugin.name} enabled`}
          aria-describedby={blocked ? `${plugin.id}-problem` : undefined}
          disabled={busy || blocked}
          onClick={onToggle}
        >
          <span className={styles.knob} aria-hidden="true" />
        </button>
        <div className={styles.pluginText}>
          <div className={styles.pluginTitle}>
            <strong>{plugin.name}</strong>
            <span className={styles.version}>{plugin.version}</span>
            {plugin.author ? <span className={styles.author}>by {plugin.author}</span> : null}
            <Pill tone={display.tone}>{pending ? 'Saving…' : display.label}</Pill>
            {plugin.dev ? <Pill tone="info">Dev</Pill> : null}
          </div>
          {plugin.dev ? <p className={styles.devPath} title={plugin.dev.path}>Loaded from <code>{plugin.dev.path}</code></p> : null}
          {plugin.dev?.problem ? (
            <p className={styles.problemNote} role="status">
              <TriangleAlert size={13} aria-hidden="true" />
              <span>The folder's latest change was not loaded: {plugin.dev.problem}</span>
            </p>
          ) : null}
          {plugin.description ? <p className={styles.description}>{plugin.description}</p> : null}
          <div className={styles.chips} aria-label="Permissions">
            {declared.length === 0 ? <span className={styles.muted}>No permissions</span> : null}
            {declared.map((permission) => {
              const spec = permissionSpec(permission)
              const on = granted.has(permission)
              return (
                <span key={permission} className={styles.chip} data-granted={on || undefined} data-risk={spec.risk} title={`${spec.title}${on ? '' : ' (not granted)'}: ${spec.description}`}>
                  {on ? null : <span className="sr-only">Not granted: </span>}
                  {permission}
                </span>
              )
            })}
          </div>
          {blocked ? (
            <p id={`${plugin.id}-problem`} className={styles.problemNote}>
              <TriangleAlert size={13} aria-hidden="true" />
              <span>{plugin.problem}</span>
            </p>
          ) : null}
          {!blocked && (state === 'crashed' || (state === 'stopped' && runtime?.reason)) ? (
            <p className={styles.reason} data-tone={state === 'crashed' ? 'danger' : undefined}>{runtime?.reason}</p>
          ) : null}
        </div>
        <div className={styles.pluginMeta}>
          <span>{formatBytes(plugin.sizeBytes)}</span>
          <span>{contents.join(' · ') || 'empty'}</span>
        </div>
        <div className={styles.pluginActions}>
          {canRestart ? (
            <Button variant="ghost" disabled={busy} onClick={onRestart} title="Restart the script from its stored code">
              <RotateCcw size={13} aria-hidden="true" />Restart
            </Button>
          ) : null}
          {plugin.hasSettings && !blocked ? (
            <Button variant="ghost" disabled={busy} onClick={onSettings} aria-label={`${plugin.name} settings`}>
              <SlidersHorizontal size={13} aria-hidden="true" />Settings
            </Button>
          ) : null}
          {plugin.dev ? (
            <Button variant="ghost" disabled={busy} onClick={onUnlink} title="Stop reloading from the folder; keep the last loaded version">
              <Link2Off size={13} aria-hidden="true" />Unlink
            </Button>
          ) : null}
          {onUpdate ? (
            <Button variant="ghost" disabled={busy} onClick={onUpdate} title="Download the package again from the link it was installed from and review it">
              <RefreshCw size={13} aria-hidden="true" />Update from link
            </Button>
          ) : null}
          {onSave && !blocked ? (
            <Button variant="ghost" disabled={busy} aria-busy={saving || undefined} onClick={onSave} aria-label={`Save ${plugin.name} package`} title="Save the installed package as a file, without settings values or stored data">
              <Download size={13} aria-hidden="true" />{saving ? 'Saving…' : 'Save package'}
            </Button>
          ) : null}
          <Button variant="ghost" disabled={busy} onClick={onPermissions}>
            <ShieldCheck size={13} aria-hidden="true" />Permissions
          </Button>
          <Button variant="ghost" aria-expanded={expanded} aria-controls={details} onClick={onExpand}>
            <ChevronDown size={13} aria-hidden="true" className={styles.chevron} data-open={expanded || undefined} />Details
          </Button>
          <Button variant="ghost" className={styles.danger} disabled={busy} onClick={onRemove} aria-label={`Remove ${plugin.name}`} title="Remove">
            <Trash2 size={13} aria-hidden="true" />
          </Button>
        </div>
      </div>
      {expanded ? <PluginDetails id={details} plugin={plugin} runtime={runtime} /> : null}
    </li>
  )
}

function PluginDetails({ id, plugin, runtime }: { readonly id: string; readonly plugin: PluginSummary; readonly runtime: PluginRuntime | undefined }) {
  const logs = runtime?.logs ?? []
  return (
    <div id={id} className={styles.details}>
      <dl className={styles.facts}>
        <div><dt>Identifier</dt><dd><code>{plugin.id}</code></dd></div>
        {plugin.homepage ? (
          <div>
            <dt>Homepage</dt>
            <dd>
              <button type="button" className={styles.homepage} onClick={() => void openExternal(plugin.homepage)} title="Opens in your browser">
                <span>{plugin.homepage}</span><ExternalLink size={12} aria-hidden="true" />
              </button>
            </dd>
          </div>
        ) : null}
        {plugin.source ? (
          <div>
            <dt>Installed from</dt>
            <dd>
              <button type="button" className={styles.homepage} onClick={() => void openExternal(plugin.source)} title="Opens in your browser">
                <span>{plugin.source}</span><ExternalLink size={12} aria-hidden="true" />
              </button>
            </dd>
          </div>
        ) : null}
        {plugin.minAppVersion ? <div><dt>Needs app</dt><dd>{plugin.minAppVersion} or later</dd></div> : null}
        <div><dt>Installed</dt><dd>{formatDate(plugin.installedAt)}</dd></div>
        <div><dt>Updated</dt><dd>{formatDate(plugin.updatedAt)}</dd></div>
        {runtime ? (
          <div>
            <dt>Contributes</dt>
            <dd>
              {[
                runtime.pages.length ? `${runtime.pages.length} ${runtime.pages.length === 1 ? 'page' : 'pages'}` : '',
                runtime.commands ? `${runtime.commands} ${runtime.commands === 1 ? 'command' : 'commands'}` : '',
                runtime.status ? `${runtime.status} status ${runtime.status === 1 ? 'readout' : 'readouts'}` : '',
              ].filter(Boolean).join(' · ') || 'Nothing yet'}
            </dd>
          </div>
        ) : null}
        {runtime?.faults ? <div><dt>Handler errors</dt><dd>{runtime.faults}</dd></div> : null}
      </dl>
      {runtime?.pages.length ? (
        <div className={styles.pageLinks}>
          {runtime.pages.map((page) => <a key={page.key} href={`#plugin/${page.key}`}>{page.title}</a>)}
        </div>
      ) : null}
      <div className={styles.logHeader}>
        <h3>Log</h3>
        <span className={styles.muted}>{logs.length ? `Last ${logs.length} lines, this session only` : ''}</span>
      </div>
      {logs.length === 0 ? <p className={styles.muted}>{plugin.hasScript ? 'Nothing logged yet.' : 'This plugin has no script, so it never logs.'}</p> : <LogView logs={logs} />}
    </div>
  )
}

function LogView({ logs }: { readonly logs: readonly LogEntry[] }) {
  return (
    <ol className={styles.log} aria-label="Plugin log" tabIndex={0}>
      {logs.map((entry, index) => (
        <li key={`${entry.at}:${index}`} data-level={entry.level}>
          <time dateTime={new Date(entry.at).toISOString()}>{formatTime(entry.at)}</time>
          <span className={styles.level}>{entry.level}</span>
          <span className={styles.logText}>{entry.text}</span>
        </li>
      ))}
    </ol>
  )
}

function formatDate(value: string): string {
  const at = knownTime(value)
  return at === null ? 'Unknown' : new Date(at).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
}

function formatTime(at: number): string {
  return new Date(at).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false })
}
