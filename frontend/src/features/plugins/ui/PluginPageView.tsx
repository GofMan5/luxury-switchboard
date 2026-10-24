import { useCallback, useEffect } from 'react'
import { LifeBuoy, Puzzle, RotateCcw } from 'lucide-react'
import { pluginIcon } from '../../../app/plugin-icons'
import { EmptyState } from '../../../shared/ui/chrome'
import { Button } from '../../../shared/ui/Button'
import { restartInSafeMode } from '../../../shared/runtime/safe-mode'
import type { PageEvent } from '../domain/protocol'
import { splitPageKey } from '../domain/plugin'
import { PluginTree } from './PluginTree'
import { usePlugins } from './usePlugins'
import styles from './PluginsPage.module.css'

/**
 * A workspace page a plugin registered. The host draws the frame — title,
 * owner, state — and the plugin's checked tree inside it; the plugin is told
 * when its page is shown and hidden so it can refresh only while visible.
 */
export default function PluginPageView({ pageId }: { readonly pageId: string }) {
  const { model, state } = usePlugins()
  const page = state.pages[pageId]
  const parts = splitPageKey(pageId)
  const plugin = parts ? state.plugins.find((entry) => entry.id === parts.pluginId) : undefined
  const runtime = plugin ? state.runtimes[plugin.id] : undefined
  const running = runtime?.state === 'running'
  const registered = page !== undefined

  const send = useCallback((event: PageEvent) => model.sendPageEvent(pageId, event), [model, pageId])

  useEffect(() => {
    if (!registered || !running) return
    send({ kind: 'show', form: {} })
    return () => send({ kind: 'hide', form: {} })
  }, [registered, running, send])

  if (!page) {
    const reason = !parts ? 'This address does not name a plugin page.'
      : state.phase === 'loading' ? 'Loading plugins…'
        : !plugin ? 'No installed plugin has this page. It may have been removed.'
          : !plugin.enabled ? `${plugin.name} is turned off. Turn it on in Plugins to use its pages.`
            : state.safeMode ? 'Safe mode is on, so plugins are not running.'
              : runtime?.state === 'starting' ? `${plugin.name} is starting…`
                : runtime?.state === 'crashed' ? `${plugin.name} stopped: ${runtime.reason}`
                  : `${plugin.name} does not register this page.`
    return (
      <section className={styles.page}>
        <header className="page-header">
          <div>
            <h1>{plugin?.name ?? 'Plugin page'}</h1>
            <p>{parts ? `#plugin/${pageId}` : 'Unknown page'}</p>
          </div>
          <div className={styles.headerActions}>
            {state.safeMode ? <Button onClick={() => restartInSafeMode(false)}><LifeBuoy size={14} />Leave safe mode</Button> : null}
            {plugin?.enabled && runtime?.state === 'crashed' && !state.safeMode ? <Button onClick={() => void model.restart(plugin.id)}><RotateCcw size={14} />Restart</Button> : null}
            <Button onClick={() => { window.location.hash = 'plugins' }}>Open Plugins</Button>
          </div>
        </header>
        <div className="page-body">
          <EmptyState icon={Puzzle} title="This page is not registered" hint={reason} height={240} />
        </div>
      </section>
    )
  }

  const Icon = pluginIcon(page.icon)
  return (
    <section className={styles.page} aria-label={page.title} data-plugin-page={page.pluginId} data-page-id={page.pageId}>
      <header className="page-header">
        <div>
          <h1>{page.title}</h1>
          <p className={styles.owner}>
            <Icon size={12} strokeWidth={1.8} />
            <span>From the {page.pluginName} plugin{plugin ? ` · ${plugin.version}` : ''}</span>
            {runtime && runtime.state !== 'running' ? <span className={styles.stateNote} data-state={runtime.state}>{stateLabel(runtime.state)}</span> : null}
          </p>
        </div>
        {runtime?.state === 'crashed' && plugin ? (
          <div className={styles.headerActions}>
            <Button onClick={() => void model.restart(plugin.id)}><RotateCcw size={14} />Restart</Button>
          </div>
        ) : null}
      </header>
      <div className="page-body">
        {page.problem ? <p className={styles.notice} data-tone="danger" role="alert">The plugin sent a page this window cannot draw: {page.problem}</p> : null}
        {page.tree ? <PluginTree tree={page.tree} disabled={!running} onEvent={send} /> : (
          <EmptyState icon={Icon} title={`${page.title} is loading`} hint="The plugin has not drawn this page yet." height={240} />
        )}
      </div>
    </section>
  )
}

function stateLabel(state: string): string {
  switch (state) {
    case 'starting': return 'Starting…'
    case 'crashed': return 'Stopped — controls are inactive'
    default: return 'Not running'
  }
}
