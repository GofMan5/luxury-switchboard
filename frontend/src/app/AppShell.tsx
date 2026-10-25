import type { PropsWithChildren } from 'react'
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react'
import { ArrowUpCircle, Command, LifeBuoy, PanelLeftClose, PanelLeftOpen, Play, Square } from 'lucide-react'
import { useProviders } from '../features/providers/ui/useProviders'
import { useRelay } from '../features/relay/ui/useRelay'
import { useSettings } from '../features/settings/ui/useSettings'
import { NotificationsSurface } from '../features/notifications/ui/NotificationsSurface'
import { UpdateDialog } from '../features/updates/ui/UpdateDialog'
import { PluginQuestionSurface } from '../features/plugins/ui/PluginQuestion'
import { useAppServices } from './services'
import { markBootReady } from './boot'
import { Button } from '../shared/ui/Button'
import { StatusDot } from '../shared/ui/StatusDot'
import { WorkspaceBoundary } from '../shared/ui/WorkspaceBoundary'
import { useMediaQuery } from '../shared/ui/useMediaQuery'
import { restartInSafeMode, safeMode } from '../shared/runtime/safe-mode'
import { visibleNavigation, type AppRoute } from './navigation'
import { arrangeSections, railGroups, railItemName } from './rail'
import { pluginIcon } from './plugin-icons'
import { actionsFor } from '../shared/contracts/extensions'
import { boundCommand, shortcutBindings } from './plugin-hotkeys'
import { overflowSummary, readoutLimit, readoutOwner, splitReadouts } from './status-readouts'
import { CommandPalette } from './CommandPalette'
import { useShellCommands } from './useShellCommands'
import styles from './AppShell.module.css'

interface AppShellProps extends PropsWithChildren {
  readonly route: AppRoute
  readonly onNavigate: (route: AppRoute) => void
}

const EMPTY_PROVIDERS: readonly never[] = []
/** Header buttons are icon-only and share the bar with the relay switch. */
const HEADER_ACTION_LIMIT = 2

export function AppShell({ route, onNavigate, children }: AppShellProps) {
  // The shell committing is the moment the boot sentinel can stand down:
  // before this, a dead bundle would have left the window black. It fires
  // once, after the first real frame, never again on rerender.
  useEffect(() => { markBootReady() }, [])
  const { model: relay, state: relayState } = useRelay()
  const { model: providersModel, state: providersState } = useProviders()
  const { state: settingsState } = useSettings()
  const { appVersion, capabilities, extensions } = useAppServices()
  const contributions = useSyncExternalStore(extensions.subscribe, extensions.snapshot)
  // Every other shell read is guarded because the boundary below wraps only the
  // workspace: a throw here is the blank-window-no-sidebar failure mode the
  // boundary exists to prevent, and the catalog shape is data from the wire.
  const providers = providersState.catalog?.providers ?? EMPTY_PROVIDERS
  const activeProvider = providers.find((provider) => provider.id === providersState.catalog?.activeId)
  // The motion switch is applied at the document root, where the CSS gates
  // every animation at once; the OS-level reduced-motion preference still wins.
  const animationsEnabled = settingsState.settings?.animationsEnabled ?? true
  useEffect(() => {
    document.documentElement.dataset.animations = animationsEnabled ? 'on' : 'off'
  }, [animationsEnabled])
  // The route at the root too, so a theme or stylesheet can restyle one page
  // (and the chrome around it) with `:root[data-route='providers']`.
  useEffect(() => {
    const root = document.documentElement
    root.dataset.route = route
    return () => { delete root.dataset.route }
  }, [route])
  // The toast master switch follows the settings record: the feed stays, the
  // interruptions stop.
  const { notifications, updates } = useAppServices()
  const updateState = useSyncExternalStore(updates.subscribe, updates.snapshot)
  const update = updateState.check
  const [updateOpen, setUpdateOpen] = useState(false)
  const notificationsEnabled = settingsState.settings?.notificationsEnabled ?? true
  useEffect(() => {
    notifications.setEnabled(notificationsEnabled)
  }, [notifications, notificationsEnabled])
  // Liveness of the active provider: the probe's dot, right where the route
  // names it. Unknown health reads as neutral — the probe runs every two
  // minutes, and silence is not an error.
  const activeHealth = providersState.health.get(activeProvider?.id ?? '')
  const healthTone = activeHealth ? (activeHealth.up ? 'completed' : 'failed') : 'stopped'
  const live = relayState.snapshot.state === 'live'
  const needsStop = live || (relayState.snapshot.state === 'error' && Boolean(relayState.snapshot.address))
  const configuredPort = settingsState.settings?.listenerPort || relayState.snapshot.port
  // The rail state survives the session: an operator who works collapsed
  // should not re-collapse on every launch.
  const [collapsed, setCollapsed] = useState(() => {
    try { return window.localStorage.getItem('switchboard.sidebar') === 'collapsed' } catch { return false }
  })
  // One scroll container serves every page, so a new page would otherwise open
  // at the previous page's offset. Reset before paint, so no frame shows it.
  const workspaceRef = useRef<HTMLElement>(null)
  useLayoutEffect(() => {
    if (workspaceRef.current) workspaceRef.current.scrollTop = 0
  }, [route])
  const toggleSidebar = useCallback(() => {
    setCollapsed((value) => {
      try { window.localStorage.setItem('switchboard.sidebar', value ? 'expanded' : 'collapsed') } catch { /* storage is a courtesy */ }
      return !value
    })
  }, [])

  const sections = useMemo(() => visibleNavigation(capabilities ?? []), [capabilities])
  const rail = useMemo(() => railGroups(arrangeSections(sections, contributions.layout), contributions.pages), [sections, contributions.layout, contributions.pages])
  const headerActions = useMemo(() => actionsFor(contributions, 'header').slice(0, HEADER_ACTION_LIMIT), [contributions])
  const [paletteOpen, setPaletteOpen] = useState(false)
  const toggleRelay = useCallback(() => relay.toggle(), [relay])
  const activate = useCallback((id: string) => providersModel.activate(id), [providersModel])
  const commands = useShellCommands({
    sections,
    pages: contributions.pages,
    navigate: onNavigate,
    relay: { live: needsStop, pending: relayState.pending, toggle: toggleRelay },
    providers,
    activeProviderId: providersState.catalog?.activeId ?? '',
    activate,
    toggleSidebar,
    contributed: contributions.commands,
  })

  // Keyboard-first: Ctrl+K opens the palette from anywhere, Ctrl+B folds the
  // rail, Alt+digit jumps straight to a page.
  const navigateRef = useRef(onNavigate)
  navigateRef.current = onNavigate
  // Keys owners asked for, read through a ref so a plugin re-registering its
  // commands does not re-attach the listener. The shell's own commands carry
  // display-only shortcuts; only contributions can ask for a binding.
  const bindings = useMemo(() => shortcutBindings(contributions.commands), [contributions.commands])
  const bindingsRef = useRef(bindings)
  bindingsRef.current = bindings
  useEffect(() => {
    const keydown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.repeat) return
      const mod = event.ctrlKey || event.metaKey
      if (mod && !event.altKey && !event.shiftKey && event.code === 'KeyK') {
        event.preventDefault()
        setPaletteOpen((open) => !open)
      } else if (mod && !event.altKey && !event.shiftKey && event.code === 'KeyB') {
        event.preventDefault()
        toggleSidebar()
      } else if (event.altKey && !mod && !event.shiftKey && /^Digit[1-9]$/u.test(event.code)) {
        const key = event.code.slice(5)
        const target = sections.flatMap((section) => section.items).find((item) => item.key === key)
        if (!target) return
        event.preventDefault()
        navigateRef.current(target.id)
      } else {
        // Owner keys come last, so the shell's own keys always win.
        const command = boundCommand(bindingsRef.current, event)
        if (!command) return
        event.preventDefault()
        // A contributed command reports its own failures (a plugin's worker
        // answers through the host); a throw here must not escape the listener.
        void Promise.resolve().then(() => command.run()).catch(() => undefined)
      }
    }
    window.addEventListener('keydown', keydown)
    return () => window.removeEventListener('keydown', keydown)
  }, [sections, toggleSidebar])

  const compactBar = useMediaQuery('(max-width: 959px)')
  const tinyBar = useMediaQuery('(max-width: 600px)')
  const readouts = splitReadouts(contributions.status, readoutLimit(compactBar, tinyBar))
  const relayLabel = live ? 'Live' : relayState.snapshot.state === 'starting' ? 'Starting' : relayState.snapshot.state === 'error' ? 'Error' : 'Stopped'

  return (
    <div className={styles.shell} data-ui="shell" data-sidebar={collapsed ? 'collapsed' : undefined}>
      <a className="skip-link" href="#workspace" onClick={(event) => { event.preventDefault(); document.getElementById('workspace')?.focus() }}>Skip to workspace</a>
      <aside className={styles.sidebar} aria-label="Sidebar" data-ui="sidebar">
        <div className={styles.brand} data-ui="sidebar-brand">
          <span className={styles.brandMark} aria-hidden="true">
            <svg viewBox="0 0 20 20" width="20" height="20" fill="none">
              <path d="M3 6h5.5l3 8H17" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
              <path d="M3 14h5.5l3-8H17" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" opacity=".45" />
              <circle cx="17" cy="14" r="1.6" fill="var(--accent)" />
            </svg>
          </span>
          <span className={styles.brandName}>Luxury Switchboard</span>
        </div>
        <button type="button" className={styles.paletteTrigger} data-ui="palette-trigger" onClick={() => setPaletteOpen(true)} aria-label="Open command palette" title="Command palette (Ctrl+K)">
          <Command size={14} aria-hidden="true" />
          <span>Search and run</span>
          <kbd>Ctrl K</kbd>
        </button>
        <nav className={styles.navigation} aria-label="Main navigation" data-ui="nav">
          {rail.map((group) => (
            <div key={group.key} className={styles.navSection} data-ui="nav-section" data-section={group.key}>
              {group.label ? <span className={styles.navSectionLabel} data-ui="nav-section-label">{group.label}</span> : null}
              {group.items.map((item) => (
                <button
                  key={item.route}
                  type="button"
                  className={styles.navItem}
                  data-ui="nav-item"
                  data-route={item.route}
                  data-active={route === item.route}
                  aria-current={route === item.route ? 'page' : undefined}
                  aria-label={railItemName(item)}
                  title={item.title}
                  onClick={() => onNavigate(item.route)}
                >
                  <item.icon size={16} strokeWidth={1.8} />
                  <span>{item.label}</span>
                  {item.badge ? (
                    <span className={styles.navBadge} data-ui="nav-badge" data-tone={item.badge.tone} aria-hidden="true">{item.badge.text}</span>
                  ) : null}
                </button>
              ))}
            </div>
          ))}
        </nav>
        <div className={styles.sidebarFooter} data-ui="sidebar-footer">
          <button
            type="button"
            className={styles.collapse}
            data-ui="sidebar-toggle"
            aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
            aria-pressed={collapsed}
            title={collapsed ? 'Expand sidebar (Ctrl+B)' : 'Collapse sidebar (Ctrl+B)'}
            onClick={toggleSidebar}
          >
            {collapsed ? <PanelLeftOpen size={15} aria-hidden="true" /> : <PanelLeftClose size={15} aria-hidden="true" />}
          </button>
          {update?.newer && update.reachable ? (
            <button
              type="button"
              className={styles.updatePill}
              data-ui="update-pill"
              aria-label={`Update to version ${update.latest} — open the update dialog`}
              title={`Version ${update.latest} is out — download and verify`}
              onClick={() => setUpdateOpen(true)}
            >
              <ArrowUpCircle size={12} aria-hidden="true" />
              <span>v{update.latest}</span>
            </button>
          ) : null}
          <span className={styles.version} data-ui="app-version" title={appVersion ? `Luxury Switchboard ${appVersion}` : 'Luxury Switchboard'}>{appVersion ? `v${appVersion}` : 'Luxury Switchboard'}</span>
        </div>
      </aside>

      <section className={styles.workspace} data-ui="workspace">
        <header className={styles.runtimeBar} data-ui="runtime-bar">
          <div className={styles.runtimeState}>
            <span className={styles.relayState} data-ui="relay-state" data-state={relayState.snapshot.state}>
              <StatusDot state={live ? 'live' : relayState.snapshot.state === 'error' ? 'error' : 'stopped'} />
              <strong>{relayLabel}</strong>
            </span>
            <span className={styles.divider} aria-hidden="true" />
            <span className={styles.address} data-ui="relay-address">
              {relayState.snapshot.address || (configuredPort ? `127.0.0.1:${configuredPort}` : 'Loopback listener')}
            </span>
            <span className={`${styles.divider} ${styles.routeDivider}`} aria-hidden="true" />
            <span className={styles.route} data-ui="active-route">
              <span className={styles.routeLabel}>Active route:</span> <span className={styles.routeName}>{activeProvider?.name ?? '—'}</span>
              {activeProvider ? <StatusDot state={healthTone} /> : null}
            </span>
            {readouts.shown.map((readout) => (
              <span key={readout.id} className={styles.readout} title={readout.tooltip ?? readout.text} data-ui="status-readout" data-owner={readoutOwner(readout.id) || undefined} data-tone={readout.tone ?? 'neutral'}>
                <span className={styles.readoutTick} aria-hidden="true" />
                {readout.text}
              </span>
            ))}
            {readouts.hidden.length > 0 ? (
              // Focusable so a keyboard reaches the list the count stands for.
              <span className={styles.readoutMore} data-ui="status-overflow" role="note" tabIndex={0} aria-label={overflowSummary(readouts.hidden)} title={overflowSummary(readouts.hidden)}>
                +{readouts.hidden.length}
              </span>
            ) : null}
          </div>
          <div className={styles.runtimeActions} data-ui="runtime-actions">
            {safeMode ? (
              <button type="button" className={styles.safeMode} data-ui="safe-mode-badge" onClick={() => restartInSafeMode(false)} title="Plugins, custom CSS and token overrides are off. Click to restart normally.">
                <LifeBuoy size={13} aria-hidden="true" />
                <span>Safe mode</span>
              </button>
            ) : null}
            {headerActions.map((action) => {
              const Icon = pluginIcon(action.icon)
              const name = `${action.title} (from ${action.owner})`
              return (
                <button key={action.id} type="button" className={styles.headerAction} data-ui="header-action" data-tone={action.tone ?? 'neutral'} aria-label={name} title={name} onClick={() => action.run()}>
                  <Icon size={15} strokeWidth={1.8} />
                </button>
              )
            })}
            <Button
              variant={needsStop ? 'secondary' : 'primary'}
              disabled={relayState.pending}
              aria-busy={relayState.pending || undefined}
              onClick={() => void relay.toggle()}
            >
              {needsStop ? <Square size={10} fill="currentColor" aria-hidden="true" /> : <Play size={12} fill="currentColor" aria-hidden="true" />}
              {relayState.pending ? 'Applying…' : live ? 'Stop relay' : needsStop ? 'Retry stop' : 'Start relay'}
            </Button>
            <NotificationsSurface />
          </div>
        </header>
        <main id="workspace" ref={workspaceRef} tabIndex={-1} className={styles.content} data-ui="workspace-content" data-route={route}>
          {/* Around the workspace, not the shell: a screen that throws must not
              take the sidebar with it, because navigating away is the recovery.
              Keyed on the route so that navigation discards a failed boundary. */}
          <WorkspaceBoundary key={route}>{children}</WorkspaceBoundary>
        </main>
      </section>
      {paletteOpen ? <CommandPalette commands={commands} onClose={() => setPaletteOpen(false)} /> : null}
      {updateOpen && update ? (
        <UpdateDialog check={update} state={updateState} onClose={() => setUpdateOpen(false)} onInstall={() => updates.install()} />
      ) : null}
      <PluginQuestionSurface />
    </div>
  )
}
