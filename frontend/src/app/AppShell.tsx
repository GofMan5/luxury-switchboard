import type { PropsWithChildren } from 'react'
import { useEffect } from 'react'
import { PanelLeftClose, Square } from 'lucide-react'
import { useProviders } from '../features/providers/ui/useProviders'
import { useRelay } from '../features/relay/ui/useRelay'
import { useSettings } from '../features/settings/ui/useSettings'
import { NotificationsSurface } from '../features/notifications/ui/NotificationsSurface'
import { useAppServices } from './services'
import { Button } from '../shared/ui/Button'
import { StatusDot } from '../shared/ui/StatusDot'
import { WorkspaceBoundary } from '../shared/ui/WorkspaceBoundary'
import { navigation, type AppRoute } from './navigation'
import styles from './AppShell.module.css'

interface AppShellProps extends PropsWithChildren {
  readonly route: AppRoute
  readonly onNavigate: (route: AppRoute) => void
}

export function AppShell({ route, onNavigate, children }: AppShellProps) {
  const { model: relay, state: relayState } = useRelay()
  const { state: providersState } = useProviders()
  const { state: settingsState } = useSettings()
  const { appVersion } = useAppServices()
  // Every other shell read is guarded because the boundary below wraps only the
  // workspace: a throw here is the blank-window-no-sidebar failure mode the
  // boundary exists to prevent, and the catalog shape is data from the wire.
  const activeProvider = (providersState.catalog?.providers ?? []).find(
    (provider) => provider.id === providersState.catalog?.activeId,
  )
  // The motion switch is applied at the document root, where the CSS gates
  // every animation at once; the OS-level reduced-motion preference still wins.
  const animationsEnabled = settingsState.settings?.animationsEnabled ?? true
  useEffect(() => {
    document.documentElement.dataset.animations = animationsEnabled ? 'on' : 'off'
  }, [animationsEnabled])
  // The toast master switch follows the settings record: the feed stays, the
  // interruptions stop.
  const { notifications } = useAppServices()
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

  return (
    <div className={styles.shell}>
      <aside className={styles.sidebar} aria-label="Main navigation">
        <div className={styles.brand}>
          <span className={styles.brandMark} aria-hidden="true">L</span>
          <span className={styles.brandName}>Luxury Switchboard</span>
        </div>
        <nav className={styles.navigation} aria-label="Main navigation">
          {navigation.map((section, sectionIndex) => (
            <div key={section.label || `section-${sectionIndex}`} className={styles.navSection}>
              {section.label ? <span className={styles.navSectionLabel}>{section.label}</span> : null}
              {section.items.map((item) => {
                const Icon = item.icon
                return (
                  <button
                    key={item.id}
                    type="button"
                    className={styles.navItem}
                    data-active={route === item.id}
                    aria-current={route === item.id ? 'page' : undefined}
                    aria-label={item.label}
                    title={item.label}
                    onClick={() => onNavigate(item.id)}
                  >
                    <Icon size={19} strokeWidth={1.8} />
                    <span>{item.label}</span>
                  </button>
                )
              })}
            </div>
          ))}
        </nav>
        <div className={styles.sidebarFooter}>
          <PanelLeftClose size={17} aria-hidden="true" />
          <span>{appVersion ? `v${appVersion}` : 'Luxury Switchboard'}</span>
        </div>
      </aside>

      <section className={styles.workspace}>
        <header className={styles.runtimeBar}>
          <div className={styles.runtimeState}>
            <StatusDot state={live ? 'live' : relayState.snapshot.state === 'error' ? 'error' : 'stopped'} />
            <strong>{live ? 'Live' : relayState.snapshot.state === 'starting' ? 'Starting' : relayState.snapshot.state === 'error' ? 'Error' : 'Stopped'}</strong>
            <span className={styles.divider} aria-hidden="true" />
            <span className={styles.address}>
              {relayState.snapshot.address || (configuredPort ? `127.0.0.1:${configuredPort}` : 'Loopback listener')}
            </span>
            <span className={styles.divider} aria-hidden="true" />
            <span className={styles.route}>
              Active route: {activeProvider?.name ?? '—'}
              {activeProvider ? <StatusDot state={healthTone} /> : null}
            </span>
          </div>
          <div className={styles.runtimeActions}>
            <Button
              variant={needsStop ? 'danger' : 'primary'}
              disabled={relayState.pending}
              onClick={() => void relay.toggle()}
            >
              <Square size={13} fill="currentColor" aria-hidden="true" />
              {relayState.pending ? 'Applying…' : live ? 'Stop relay' : needsStop ? 'Retry stop' : 'Start relay'}
            </Button>
            <NotificationsSurface />
          </div>
        </header>
        <main className={styles.content}>
          {/* Around the workspace, not the shell: a screen that throws must not
              take the sidebar with it, because navigating away is the recovery.
              Keyed on the route so that navigation discards a failed boundary. */}
          <WorkspaceBoundary key={route}>{children}</WorkspaceBoundary>
        </main>
      </section>
    </div>
  )
}
