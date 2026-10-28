import { useEffect, useId, useRef, useState } from 'react'
import { History, TriangleAlert } from 'lucide-react'
import { EmptyState, Panel } from '../../../shared/ui/chrome'
import { Button } from '../../../shared/ui/Button'
import { useModalFocus } from '../../../shared/ui/useModalFocus'
import type { JournalEntry, JournalKind, JournalOrigin } from '../domain/journal'
import { isPluginPermission, permissionSpec } from '../domain/permissions'
import { knownTime, type PluginSummary } from '../domain/plugin'
import type { PluginsModel } from '../application/plugins-model'
import { useStyleHold } from './useStyleHold'
import styles from './PluginsPage.module.css'

const KIND_LABELS: Record<JournalKind, string> = {
  installed: 'Installed',
  updated: 'Updated',
  enabled: 'Turned on',
  disabled: 'Turned off',
  permissions: 'Permissions changed',
  linked: 'Folder linked',
  unlinked: 'Folder unlinked',
  removed: 'Removed',
}

const ORIGIN_LABELS: Record<JournalOrigin, string> = {
  file: 'from a file',
  link: 'from a link',
  folder: 'from a folder',
}

type View =
  | { readonly phase: 'reading' }
  | { readonly phase: 'unavailable'; readonly message: string }
  | { readonly phase: 'ready'; readonly entries: readonly JournalEntry[]; readonly next: number; readonly incomplete: boolean }

/** Dated history of installs, updates, permissions, switches, folder links and removals. */
export function PluginJournal({ model, plugins }: {
  readonly model: PluginsModel
  readonly plugins: readonly PluginSummary[]
}) {
  const [pluginId, setPluginId] = useState('')
  const [view, setView] = useState<View>({ phase: 'reading' })
  const [older, setOlder] = useState<'idle' | 'loading' | string>('idle')
  const [clearing, setClearing] = useState<'closed' | 'open' | 'pending' | { readonly error: string }>('closed')
  const [reload, setReload] = useState(0)
  // A page that answers after the first page was read again belongs to the old list.
  const generation = useRef(0)

  // Every plugin change may have added an entry, so the first page is read again.
  useEffect(() => {
    let live = true
    generation.current += 1
    setOlder('idle')
    void model.loadJournal(pluginId ? { pluginId } : {}).then((result) => {
      if (!live) return
      setView(result.ok ? { phase: 'ready', ...result.page } : { phase: 'unavailable', message: result.message })
    })
    return () => { live = false }
  }, [model, plugins, pluginId, reload])

  const showOlder = (before: number) => {
    setOlder('loading')
    const asked = generation.current
    void model.loadJournal(pluginId ? { pluginId, before } : { before }).then((result) => {
      if (asked !== generation.current) return
      if (!result.ok) { setOlder(result.message); return }
      setOlder('idle')
      setView((current) => current.phase === 'ready'
        ? { phase: 'ready', entries: [...current.entries, ...result.page.entries.filter((entry) => entry.seq < before)], next: result.page.next, incomplete: current.incomplete || result.page.incomplete }
        : current)
    })
  }

  const clear = () => {
    setClearing('pending')
    void model.clearJournal().then((result) => {
      if (!result.ok) { setClearing({ error: result.message }); return }
      setClearing('closed')
      setReload((count) => count + 1)
    })
  }

  // Removed plugins stay choosable through their recorded entries.
  const choices = new Map(plugins.map((plugin) => [plugin.id, plugin.name]))
  if (view.phase === 'ready') for (const entry of view.entries) if (!choices.has(entry.pluginId)) choices.set(entry.pluginId, entry.name)
  const empty = view.phase === 'ready' && view.entries.length === 0 && !pluginId

  return (
    <Panel
      title="Activity"
      subtitle="Installs, updates, permission changes, switches and removals on this machine."
      actions={(
        <div className={styles.logTools}>
          <label className={styles.logFilter}>
            <span>Plugin</span>
            <select value={pluginId} onChange={(event) => setPluginId(event.currentTarget.value)}>
              <option value="">All plugins</option>
              {[...choices].sort((a, b) => a[1].localeCompare(b[1])).map(([id, name]) => <option key={id} value={id}>{name}</option>)}
            </select>
          </label>
          <Button disabled={view.phase === 'reading' || empty} onClick={() => setClearing('open')}>Clear…</Button>
        </div>
      )}
    >
      {view.phase === 'ready' && view.incomplete ? (
        <div className={styles.banner} data-tone="warning" role="status">
          <TriangleAlert size={14} aria-hidden="true" />
          <span>The newest entries could not be saved and will be lost when the app closes.</span>
        </div>
      ) : null}
      {view.phase === 'reading' ? <p className={styles.placeholder}>Reading…</p>
        : view.phase === 'unavailable' ? <p className={styles.placeholder} role="status">Unavailable: {view.message}</p>
          : view.entries.length === 0 ? <EmptyState icon={History} title={pluginId ? 'Nothing recorded for this plugin' : 'Nothing recorded yet'} hint="Changes to plugins appear here as they happen." />
            : (
              <>
                <ol className={styles.journal} aria-label="Plugin activity">
                  {view.entries.map((entry) => <JournalRow key={entry.seq} entry={entry} />)}
                </ol>
                {view.next > 0 ? (
                  <div className={styles.journalMore}>
                    <Button disabled={older === 'loading'} aria-busy={older === 'loading' || undefined} onClick={() => showOlder(view.next)}>
                      {older === 'loading' ? 'Reading…' : 'Show older'}
                    </Button>
                    {older !== 'idle' && older !== 'loading' ? <span className={styles.notice} data-tone="danger" role="alert">Unavailable: {older}</span> : null}
                  </div>
                ) : null}
              </>
            )}
      {clearing !== 'closed' ? (
        <ClearJournalDialog
          pending={clearing === 'pending'}
          error={typeof clearing === 'object' ? clearing.error : ''}
          onCancel={() => setClearing('closed')}
          onConfirm={clear}
        />
      ) : null}
    </Panel>
  )
}

function JournalRow({ entry }: { readonly entry: JournalEntry }) {
  const at = knownTime(entry.at)
  const version = entry.fromVersion ? `${entry.fromVersion} → ${entry.version}` : entry.version
  return (
    <li className={styles.journalRow} data-kind={entry.kind}>
      <time dateTime={entry.at} title={at === null ? undefined : new Date(at).toLocaleString(undefined, { dateStyle: 'full', timeStyle: 'medium' })}>{at === null ? 'Unknown' : new Date(at).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })}</time>
      <div className={styles.journalText}>
        <span>
          <strong>{KIND_LABELS[entry.kind]}</strong> {entry.name}
          {version ? <span className={styles.muted}> {version}</span> : null}
          {entry.origin ? <span className={styles.muted}> {ORIGIN_LABELS[entry.origin]}</span> : null}
        </span>
        {entry.granted.length || entry.revoked.length ? (
          <span className={styles.chips}>
            {entry.granted.map((permission) => <code key={`+${permission}`} className={styles.chip} data-granted="true" title={`Granted ${permission}`}>+ {permissionLabel(permission)}</code>)}
            {entry.revoked.map((permission) => <code key={`-${permission}`} className={styles.chip} title={`Revoked ${permission}`}>− {permissionLabel(permission)}</code>)}
          </span>
        ) : null}
      </div>
    </li>
  )
}

function permissionLabel(permission: string): string {
  return isPluginPermission(permission) ? permissionSpec(permission).title : permission
}

function ClearJournalDialog({ pending, error, onCancel, onConfirm }: {
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
            <h2 id={`${id}-title`}>Clear plugin activity?</h2>
            <p id={`${id}-body`}>Every recorded entry for every plugin is deleted. Installed plugins and their data stay as they are.</p>
          </div>
        </header>
        {error ? <div className={styles.dialogBody}><p className={styles.dialogError} role="alert">{error}</p></div> : null}
        <footer>
          <Button disabled={pending} data-autofocus onClick={onCancel}>Cancel</Button>
          <Button variant="danger" disabled={pending} aria-busy={pending || undefined} onClick={onConfirm}>{pending ? 'Clearing…' : 'Clear'}</Button>
        </footer>
      </section>
    </div>
  )
}
