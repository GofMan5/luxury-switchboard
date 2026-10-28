import { useEffect, useRef, useState, useSyncExternalStore, type ChangeEvent, type ReactNode } from 'react'
import { ChevronDown, ClipboardPaste, Copy, Download, ExternalLink, FolderOpen, LayoutGrid, LifeBuoy, Link2, Link2Off, Puzzle, RefreshCw, RotateCcw, ShieldCheck, SlidersHorizontal, Trash2, TriangleAlert, Upload, X } from 'lucide-react'
import { EmptyState, Panel, Pill, type PillTone } from '../../../shared/ui/chrome'
import { Button } from '../../../shared/ui/Button'
import { formatBytes } from '../../../shared/format/metrics'
import { restartInSafeMode } from '../../../shared/runtime/safe-mode'
import { openExternal } from '../../../platform/lifecycle/open-external'
import { useAppServices } from '../../../app/services'
import { EXAMPLE_PLUGINS } from '../domain/examples'
import { PLUGIN_LIMIT, parsePackage, type PluginPackage } from '../domain/manifest'
import { isPluginPermission, permissionSpec } from '../domain/permissions'
import { filterLog, knownTime, type LogEntry, type LogLevel, type PluginRuntime, type PluginSummary, type RuntimeState } from '../domain/plugin'
import { ClearDataDialog, DevLinkDialog, InstallReviewDialog, LinkDialog, PasteDialog, PermissionsDialog, ProblemsDialog, RemoveDialog } from './PluginDialogs'
import { PluginJournal } from './PluginJournal'
import { PluginReference } from './PluginReference'
import { PluginSettingsDialog } from './PluginSettingsDialog'
import { sortUpdates, type DataResult, type PluginDataUsage, type UpdateCheck } from '../application/plugins-model'
import { usePlugins } from './usePlugins'
import styles from './PluginsPage.module.css'

type Dialog =
  | { readonly kind: 'paste' }
  | { readonly kind: 'dev'; readonly error: string }
  | { readonly kind: 'link'; readonly error: string; readonly problems: readonly string[]; readonly url?: string; readonly expectedId?: string }
  | { readonly kind: 'review'; readonly pkg: PluginPackage; readonly error: string; readonly source?: string; readonly expectedId?: string }
  | { readonly kind: 'problems'; readonly title: string; readonly problems: readonly string[] }
  | { readonly kind: 'permissions'; readonly id: string }
  | { readonly kind: 'settings'; readonly id: string }
  | { readonly kind: 'remove'; readonly id: string }
  | { readonly kind: 'clear'; readonly id: string }

/** Files larger than this cannot be a valid package (one command frame). */
const FILE_LIMIT = 512 * 1024

export default function PluginsPage() {
  const { model, state } = usePlugins()
  const { appVersion, capabilities, extensions } = useAppServices()
  const layout = useSyncExternalStore(extensions.subscribe, extensions.snapshot).layout
  const [dialog, setDialog] = useState<Dialog | null>(null)
  const [notice, setNotice] = useState('')
  // Bumped after a clear so the open details read the emptied storage again.
  const [dataRevision, setDataRevision] = useState(0)
  const [clearError, setClearError] = useState('')
  const [expanded, setExpanded] = useState<string>('')
  const [saving, setSaving] = useState('')
  const fileInput = useRef<HTMLInputElement>(null)
  const busy = state.pending !== ''
  const full = state.plugins.length >= PLUGIN_LIMIT
  const empty = state.phase === 'ready' && state.plugins.length === 0

  const review = (pkg: PluginPackage, source?: string, expectedId?: string) => {
    model.clearError()
    setNotice('')
    setDialog({ kind: 'review', pkg, error: '', source, expectedId })
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

  // An update names the plugin it is for, so the review refuses a link that
  // now serves a different package.
  const download = async (url: string, expectedId?: string) => {
    const result = await model.download(url)
    if (result.ok) review(result.pkg, result.source, expectedId)
    else setDialog({ kind: 'link', error: result.message, problems: result.problems ?? [], url, expectedId })
  }

  const update = (plugin: PluginSummary) => {
    model.clearError()
    setNotice('')
    const offer = state.updates[plugin.id]
    if (offer?.state === 'newer' || offer?.state === 'changed') review(offer.pkg, offer.source, plugin.id)
    else void download(plugin.source, plugin.id)
  }

  const checkUpdates = async () => {
    model.clearError()
    setNotice('')
    const { newer, changed, failed, checked } = await model.checkUpdates()
    if (checked === 0) return
    const found = newer > 0 ? `${newer} ${newer === 1 ? 'update' : 'updates'} available.` : failed === 0 && changed === 0 ? 'All linked plugins are up to date.' : 'No updates found.'
    const parts = [found]
    if (changed > 0) parts.push(`${changed} ${changed === 1 ? 'link serves' : 'links serve'} changed code under the installed version; review before reinstalling.`)
    if (failed > 0) parts.push(`${failed} could not be checked.`)
    setNotice(parts.join(' '))
  }

  const updateAll = async () => {
    model.clearError()
    setNotice('')
    const { updated, review, failed } = await model.updateAll(appVersion)
    const plural = (count: number) => `${count} ${count === 1 ? 'plugin' : 'plugins'}`
    const parts = [
      updated.length ? `Updated ${updated.length === 1 ? `${updated[0]?.name} to ${updated[0]?.version}` : plural(updated.length)}.` : '',
      review.length ? `${plural(review.length)} ${review.length === 1 ? 'needs' : 'need'} your review: use Update on ${review.length === 1 ? 'its row' : 'their rows'}.` : '',
      failed.length === 1 ? `${failed[0]?.id} could not be updated: ${failed[0]?.message}` : failed.length ? `${plural(failed.length)} could not be updated.` : '',
    ]
    setNotice(parts.filter(Boolean).join(' '))
  }

  const install = async (pkg: PluginPackage, granted: readonly string[], source?: string, expectedId?: string) => {
    const upgrade = state.plugins.some((plugin) => plugin.id === pkg.id)
    const result = await model.install(pkg, granted, source)
    if (result.ok) {
      setDialog(null)
      setNotice(`${result.plugin.name} ${result.plugin.version} ${upgrade ? 'updated' : 'installed'}.`)
    } else {
      setDialog({ kind: 'review', pkg, error: result.message, source, expectedId })
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

  const linked = capabilities.includes('plugins.download') && state.plugins.some((plugin) => plugin.source !== '' && !plugin.dev)
  const offers = linked ? sortUpdates(state.plugins, state.updates, appVersion).quiet.length : 0

  const current = dialog && (dialog.kind === 'permissions' || dialog.kind === 'settings' || dialog.kind === 'remove' || dialog.kind === 'clear')
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
          <Button variant="primary" onClick={() => fileInput.current?.click()} disabled={busy || state.phase !== 'ready'}>
            <Upload size={14} aria-hidden="true" />Install from file
          </Button>
          {capabilities.includes('plugins.download') ? (
            <Button onClick={() => { model.clearError(); setNotice(''); setDialog({ kind: 'link', error: '', problems: [] }) }} disabled={busy || state.phase !== 'ready'}>
              <Link2 size={14} aria-hidden="true" />Install from link…
            </Button>
          ) : null}
          {linked ? (
            <Button onClick={() => void checkUpdates()} disabled={busy || state.checking || state.phase !== 'ready'} aria-busy={state.checking || undefined} title="Download each linked plugin's package and compare its version; nothing is installed">
              <RefreshCw size={14} aria-hidden="true" />{state.checking ? 'Checking…' : 'Check for updates'}
            </Button>
          ) : null}
          {offers > 0 || state.pending === 'update-all' ? (
            <Button onClick={() => void updateAll()} disabled={busy || state.checking || state.phase !== 'ready'} aria-busy={state.pending === 'update-all' || undefined} title="Install every update that asks for nothing new; one that asks for new permissions or hosts, needs a newer app or belongs to a turned-off plugin waits for your review">
              <Download size={14} aria-hidden="true" />{state.pending === 'update-all' ? 'Updating…' : `Update all (${offers})`}
            </Button>
          ) : null}
          <Button variant="ghost" onClick={() => { setNotice(''); setDialog({ kind: 'paste' }) }} disabled={busy || state.phase !== 'ready'}>
            <ClipboardPaste size={14} aria-hidden="true" />Paste JSON
          </Button>
          <Button variant="ghost" onClick={() => { model.clearError(); setNotice(''); setDialog({ kind: 'dev', error: '' }) }} disabled={busy || state.phase !== 'ready'}>
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
                  loadData={() => model.loadData(plugin.id)}
                  dataRevision={dataRevision}
                  onCopyLog={(entries) => model.copyLog(entries)}
                  onSaveLog={(entries) => model.saveLog(plugin.id, entries)}
                  onClearData={() => { setClearError(''); setNotice(''); setDialog({ kind: 'clear', id: plugin.id }) }}
                  onUpdate={plugin.source && !plugin.dev && capabilities.includes('plugins.download') ? () => update(plugin) : undefined}
                  update={state.updates[plugin.id]}
                  saving={saving === plugin.id}
                  onSave={capabilities.includes('plugins.export') ? () => void savePackage(plugin) : undefined}
                  onUnlink={() => { setNotice(''); void model.devUnlink(plugin.id).then((ok) => { if (ok) setNotice(`${plugin.name} is no longer loaded from its folder; the last loaded version stays installed.`) }) }}
                />
              ))}
            </ul>
          ) : null}
        </Panel>

        {capabilities.includes('plugins.journal') ? <PluginJournal model={model} plugins={state.plugins} /> : null}

        <Disclosure title="Examples" subtitle="Ready-made packages. Installing one opens the same review as any other." open={empty}>
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
        </Disclosure>

        <Disclosure title="Developer reference" subtitle="Everything a package can contain and do." open={empty}>
          <PluginReference />
        </Disclosure>
      </div>

      {dialog?.kind === 'dev' ? <DevLinkDialog pending={state.pending === 'install'} error={dialog.error} onCancel={() => setDialog(null)} onLink={(path) => void link(path)} /> : null}
      {dialog?.kind === 'link' ? <LinkDialog pending={state.pending === 'install'} error={dialog.error} problems={dialog.problems} initialUrl={dialog.url} onCancel={() => setDialog(null)} onDownload={(url) => void download(url, dialog.expectedId)} /> : null}
      {dialog?.kind === 'paste' ? <PasteDialog onCancel={() => setDialog(null)} onReview={review} /> : null}
      {dialog?.kind === 'problems' ? <ProblemsDialog title={dialog.title} problems={dialog.problems} onClose={() => setDialog(null)} /> : null}
      {dialog?.kind === 'review' ? (
        <InstallReviewDialog
          pkg={dialog.pkg}
          existing={state.plugins.find((plugin) => plugin.id === dialog.pkg.id)}
          appVersion={appVersion}
          source={dialog.source}
          expectedId={dialog.expectedId}
          pending={state.pending === 'install'}
          error={dialog.error || (!state.plugins.some((plugin) => plugin.id === dialog.pkg.id) && full ? `At most ${PLUGIN_LIMIT} plugins can be installed; remove one first.` : '')}
          onCancel={() => setDialog(null)}
          onInstall={(granted) => void install(dialog.pkg, granted, dialog.source, dialog.expectedId)}
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
      {dialog?.kind === 'clear' && current ? (
        <ClearDataDialog
          plugin={current}
          pending={state.pending === current.id}
          error={clearError}
          onCancel={() => setDialog(null)}
          onConfirm={() => void model.clearData(current.id).then((result) => {
            setDataRevision((value) => value + 1)
            if (!result.ok) { setClearError(result.message); return }
            setDialog(null)
            setNotice(`Stored data for ${current.name} cleared.`)
          })}
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

function PluginRow({ plugin, runtime, pending, busy, safeMode, expanded, saving, onExpand, onToggle, onPermissions, onSettings, onRestart, onRemove, onUnlink, onUpdate, update, onSave, loadData, dataRevision, onClearData, onCopyLog, onSaveLog }: {
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
  readonly loadData: () => Promise<DataResult>
  readonly dataRevision: number
  readonly onClearData: () => void
  readonly onCopyLog: (entries: readonly LogEntry[]) => Promise<boolean>
  readonly onSaveLog: (entries: readonly LogEntry[]) => Promise<boolean>
  readonly saving: boolean
  /** Reinstalls from the link the plugin came from; absent without one. */
  readonly onUpdate?: () => void
  /** The last check of that link, if one ran. */
  readonly update?: UpdateCheck
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
            {update?.state === 'newer' ? <Pill tone="info">Update {update.pkg.version} available</Pill> : null}
            {update?.state === 'changed' ? <Pill tone="warning">Code changed without a new version</Pill> : null}
          </div>
          {plugin.dev ? <p className={styles.devPath} title={plugin.dev.path}>Loaded from <code>{plugin.dev.path}</code></p> : null}
          {plugin.dev?.problem ? (
            <p className={styles.problemNote} role="status">
              <TriangleAlert size={13} aria-hidden="true" />
              <span>The folder's latest change was not loaded: {plugin.dev.problem}</span>
            </p>
          ) : null}
          {update?.state === 'error' ? (
            <p className={styles.problemNote} role="status">
              <TriangleAlert size={13} aria-hidden="true" />
              <span>The update check failed: {update.message}</span>
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
        <div className={styles.pluginActions}>
          {canRestart && state === 'crashed' ? (
            <Button variant="ghost" disabled={busy} onClick={onRestart} title="Restart the script from its stored code">
              <RotateCcw size={13} aria-hidden="true" />Restart
            </Button>
          ) : null}
          {onUpdate && (update?.state === 'newer' || update?.state === 'changed') ? (
            <Button variant="ghost" disabled={busy} onClick={onUpdate} title="Download the package again from the link it was installed from and review it">
              <RefreshCw size={13} aria-hidden="true" />{update.state === 'newer' ? `Update to ${update.pkg.version}` : 'Review changed code'}
            </Button>
          ) : null}
          {plugin.hasSettings && !blocked ? (
            <Button variant="ghost" disabled={busy} onClick={onSettings} aria-label={`${plugin.name} settings`}>
              <SlidersHorizontal size={13} aria-hidden="true" />Settings
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
      {expanded ? (
        <PluginDetails id={details} plugin={plugin} runtime={runtime} contents={contents} onCopyLog={onCopyLog} onSaveLog={onSaveLog}>
          {(canRestart && state !== 'crashed') || onUpdate || (onSave && !blocked) || plugin.dev ? (
            <div className={styles.detailActions} aria-label="Actions" role="group">
              {canRestart && state !== 'crashed' ? (
                <Button disabled={busy} onClick={onRestart} title="Restart the script from its stored code">
                  <RotateCcw size={13} aria-hidden="true" />Restart
                </Button>
              ) : null}
              {onUpdate && update?.state !== 'newer' && update?.state !== 'changed' ? (
                <Button disabled={busy} onClick={onUpdate} title="Download the package again from the link it was installed from and review it">
                  <RefreshCw size={13} aria-hidden="true" />Update from link
                </Button>
              ) : null}
              {onSave && !blocked ? (
                <Button disabled={busy} aria-busy={saving || undefined} onClick={onSave} aria-label={`Save ${plugin.name} package`} title="Save the installed package as a file, without settings values or stored data">
                  <Download size={13} aria-hidden="true" />{saving ? 'Saving…' : 'Save package'}
                </Button>
              ) : null}
              {plugin.dev ? (
                <Button disabled={busy} onClick={onUnlink} title="Stop reloading from the folder; keep the last loaded version">
                  <Link2Off size={13} aria-hidden="true" />Unlink
                </Button>
              ) : null}
            </div>
          ) : null}
          <StoredData load={loadData} revision={dataRevision} disabled={busy} onClear={onClearData} />
        </PluginDetails>
      ) : null}
    </li>
  )
}

function PluginDetails({ id, plugin, runtime, contents, children, onCopyLog, onSaveLog }: {
  readonly id: string
  readonly plugin: PluginSummary
  readonly runtime: PluginRuntime | undefined
  readonly contents: readonly string[]
  readonly children: ReactNode
  readonly onCopyLog: (entries: readonly LogEntry[]) => Promise<boolean>
  readonly onSaveLog: (entries: readonly LogEntry[]) => Promise<boolean>
}) {
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
        <div><dt>Size</dt><dd>{formatBytes(plugin.sizeBytes)}</dd></div>
        <div><dt>Contains</dt><dd>{contents.join(', ') || 'Nothing'}</dd></div>
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
      {children}
      {runtime?.pages.length ? (
        <div className={styles.pageLinks}>
          {runtime.pages.map((page) => <a key={page.key} href={`#plugin/${page.key}`}>{page.title}</a>)}
        </div>
      ) : null}
      <LogSection logs={runtime?.logs ?? []} hasScript={plugin.hasScript} onCopy={onCopyLog} onSave={onSaveLog} />
    </div>
  )
}

/** A panel that folds away once it stops being the main thing on the page. */
function Disclosure({ title, subtitle, open, children }: {
  readonly title: string
  readonly subtitle: string
  readonly open: boolean
  readonly children: ReactNode
}) {
  const [shown, setShown] = useState(open)
  const [previous, setPrevious] = useState(open)
  if (previous !== open) {
    setPrevious(open)
    // Opens when the list empties; never folds away under the user right after an install.
    if (open) setShown(true)
  }
  return (
    <details className={styles.disclosure} open={shown} onToggle={(event) => setShown(event.currentTarget.open)}>
      <summary>
        <ChevronDown size={14} aria-hidden="true" className={styles.chevron} data-open={shown || undefined} />
        <span className={styles.disclosureTitle}><strong>{title}</strong><span>{subtitle}</span></span>
      </summary>
      {shown ? children : null}
    </details>
  )
}

/** What the plugin keeps on this machine, with a way to start it afresh. */
function StoredData({ load, revision, disabled, onClear }: {
  readonly load: () => Promise<DataResult>
  readonly revision: number
  readonly disabled: boolean
  readonly onClear: () => void
}) {
  const [result, setResult] = useState<DataResult | null>(null)
  const loadRef = useRef(load)
  loadRef.current = load
  useEffect(() => {
    let live = true
    void loadRef.current().then((answer) => { if (live) setResult(answer) })
    return () => { live = false }
  }, [revision])
  const usage = result?.ok ? result.usage : null
  return (
    <div className={styles.dataRow}>
      <div className={styles.dataText}>
        <h3>Stored data</h3>
        {result === null ? <p className={styles.muted}>Reading…</p>
          : result.ok ? <DataSummary usage={result.usage} />
            : <p className={styles.muted} role="status">Unavailable: {result.message}</p>}
      </div>
      <Button disabled={disabled || !usage || isEmpty(usage)} onClick={onClear}>Clear data…</Button>
    </div>
  )
}

function DataSummary({ usage }: { readonly usage: PluginDataUsage }) {
  const { storage, collections } = usage
  const documents = collections.collections.reduce((sum, entry) => sum + entry.count, 0)
  const sets = collections.collections.length
  // formatBytes shows a dash for zero, which reads as unknown here.
  const size = (bytes: number) => bytes > 0 ? formatBytes(bytes) : '0 B'
  return (
    <ul className={styles.dataFacts}>
      <li>{storage.values === 0 ? 'No values' : `${storage.values} ${storage.values === 1 ? 'value' : 'values'}`}, {size(storage.bytes)} of {formatBytes(storage.limitBytes)} including settings</li>
      <li>{documents === 0 ? 'No documents' : `${documents} ${documents === 1 ? 'document' : 'documents'} in ${sets} ${sets === 1 ? 'collection' : 'collections'}`}, {size(collections.bytes)} of {formatBytes(collections.limitBytes)}</li>
    </ul>
  )
}

function isEmpty(usage: PluginDataUsage): boolean {
  return usage.storage.values === 0 && usage.collections.collections.length === 0
}

const LEVEL_CHOICES: readonly { readonly value: LogLevel; readonly label: string }[] = [
  { value: 'debug', label: 'All levels' },
  { value: 'info', label: 'Info and above' },
  { value: 'warn', label: 'Warnings and errors' },
  { value: 'error', label: 'Errors only' },
]

/** The session log with a level filter; copy and save take the lines shown. */
function LogSection({ logs, hasScript, onCopy, onSave }: {
  readonly logs: readonly LogEntry[]
  readonly hasScript: boolean
  readonly onCopy: (entries: readonly LogEntry[]) => Promise<boolean>
  readonly onSave: (entries: readonly LogEntry[]) => Promise<boolean>
}) {
  const [least, setLeast] = useState<LogLevel>('debug')
  const [done, setDone] = useState('')
  const [working, setWorking] = useState(false)
  const shown = filterLog(logs, least)
  const lines = (count: number) => `${count} ${count === 1 ? 'line' : 'lines'}`
  const run = (action: typeof onCopy, message: string) => {
    setDone('')
    setWorking(true)
    void action(shown).then((ok) => { if (ok) setDone(message) }).finally(() => setWorking(false))
  }
  const summary = logs.length === 0 ? ''
    : shown.length === logs.length ? `Last ${lines(logs.length)}, this session only`
      : `${shown.length} of ${lines(logs.length)} shown, this session only`
  return (
    <>
      <div className={styles.logHeader}>
        <h3>Log</h3>
        <span className={styles.muted} role="status">{done || summary}</span>
        {logs.length ? (
          <div className={styles.logTools}>
            <label className={styles.logFilter}>
              <span>Level</span>
              <select value={least} onChange={(event) => { setDone(''); setLeast(event.currentTarget.value as LogLevel) }}>
                {LEVEL_CHOICES.map((choice) => <option key={choice.value} value={choice.value}>{choice.label}</option>)}
              </select>
            </label>
            <Button disabled={working || shown.length === 0} onClick={() => run(onCopy, `Copied ${lines(shown.length)}.`)}>
              <Copy size={14} aria-hidden="true" />Copy
            </Button>
            <Button disabled={working || shown.length === 0} onClick={() => run(onSave, `Saved ${lines(shown.length)}.`)}>
              <Download size={14} aria-hidden="true" />Save…
            </Button>
          </div>
        ) : null}
      </div>
      {logs.length === 0 ? <p className={styles.muted}>{hasScript ? 'Nothing logged yet.' : 'This plugin has no script, so it never logs.'}</p>
        : shown.length === 0 ? <p className={styles.muted}>No lines at this level.</p>
          : <LogView logs={shown} />}
    </>
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
