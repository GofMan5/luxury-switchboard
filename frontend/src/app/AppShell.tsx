import type { PropsWithChildren } from 'react'
import { PanelLeftClose, Square } from 'lucide-react'
import { useProviders } from '../features/providers/ui/useProviders'
import { useRelay } from '../features/relay/ui/useRelay'
import { useSettings } from '../features/settings/ui/useSettings'
import { Button } from '../shared/ui/Button'
import { StatusDot } from '../shared/ui/StatusDot'
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
  const activeProvider = providersState.catalog.providers.find(
    (provider) => provider.id === providersState.catalog.activeId,
  )
  const live = relayState.snapshot.state === 'live'
  const needsStop = live || (relayState.snapshot.state === 'error' && Boolean(relayState.snapshot.address))
  const configuredPort = settingsState.settings?.listenerPort || relayState.snapshot.port

  return (
    <div className={styles.shell}>
      <aside className={styles.sidebar} aria-label="Main navigation">
        <div className={styles.brand}>
          <span className={styles.brandMark} aria-hidden="true">S</span>
          <span className={styles.brandName}>Switchboard</span>
        </div>
        <nav className={styles.navigation}>
          {navigation.map((item) => {
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
        </nav>
        <div className={styles.sidebarFooter}>
          <PanelLeftClose size={17} aria-hidden="true" />
          <span>v1.0.2</span>
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
            <span className={styles.route}>Active route: {activeProvider?.name ?? '—'}</span>
          </div>
          <Button
            variant={needsStop ? 'danger' : 'primary'}
            disabled={relayState.pending}
            onClick={() => void relay.toggle()}
          >
            <Square size={13} fill="currentColor" aria-hidden="true" />
            {relayState.pending ? 'Applying…' : live ? 'Stop relay' : needsStop ? 'Retry stop' : 'Start relay'}
          </Button>
        </header>
        <main className={styles.content}>{children}</main>
      </section>
    </div>
  )
}
