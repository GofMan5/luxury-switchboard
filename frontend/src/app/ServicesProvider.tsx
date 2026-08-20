import { useEffect, useState, type PropsWithChildren } from 'react'
import { StdioActivityPort } from '../features/activity/adapters/stdio-activity-port'
import { ActivityModel } from '../features/activity/application/activity-model'
import { StdioApiKeysPort } from '../features/api-keys/adapters/stdio-api-keys-port'
import { ApiKeysModel } from '../features/api-keys/application/api-keys-model'
import { StdioProvidersPort } from '../features/providers/adapters/stdio-providers-port'
import { ProvidersModel } from '../features/providers/application/providers-model'
import { StdioRelayPort } from '../features/relay/adapters/stdio-relay-port'
import { RelayModel } from '../features/relay/application/relay-model'
import { StdioSettingsPort } from '../features/settings/adapters/stdio-settings-port'
import { SettingsModel } from '../features/settings/application/settings-model'
import { StdioStatisticsPort } from '../features/statistics/adapters/stdio-statistics-port'
import { StatisticsModel } from '../features/statistics/application/statistics-model'
import { StdioGuardrailsPort } from '../features/guardrails/adapters/stdio-guardrails-port'
import { GuardrailsModel } from '../features/guardrails/application/guardrails-model'
import { StdioRoutesPort } from '../features/model-routes/adapters/stdio-routes-port'
import { RoutesModel } from '../features/model-routes/application/routes-model'
import { StdioTunnelPort } from '../features/tunnel/adapters/stdio-tunnel-port'
import { TunnelModel } from '../features/tunnel/application/tunnel-model'
import { StdioClientsPort } from '../features/clients/adapters/stdio-clients-port'
import { ClientsModel } from '../features/clients/application/clients-model'
import { StdioModelsPort } from '../features/models/adapters/stdio-models-port'
import { ModelsModel } from '../features/models/application/models-model'
import { StdioSharedPort } from '../features/shared-control/adapters/stdio-shared-port'
import { SharedModel } from '../features/shared-control/application/shared-model'
import { createControlPlaneSession } from '../platform/stdio/create-session'
import type { ControlPlaneSession } from '../platform/stdio/session'
import { Button } from '../shared/ui/Button'
import { ServicesContext, type AppServices } from './services'

type BootstrapState =
  | { readonly phase: 'connecting'; readonly services: null }
  | { readonly phase: 'ready'; readonly services: AppServices }
  | { readonly phase: 'error'; readonly services: null }

export function ServicesProvider({ children }: PropsWithChildren) {
  const [state, setState] = useState<BootstrapState>({ phase: 'connecting', services: null })
  const [attempt, setAttempt] = useState(0)

  useEffect(() => {
    let disposed = false
    let session: ControlPlaneSession | null = null
    let services: AppServices | null = null
    let sessionStopped = false
    let unsubscribeReconnect: (() => void) | null = null
    let reconnecting: Promise<void> | null = null
    const connectServices = (): Promise<void> => {
      if (!services) return Promise.resolve()
      if (reconnecting) return reconnecting
      const current = services
      const keys = current.apiKeys.snapshot()
      const routes = current.routes.snapshot()
      const models = current.models.snapshot()
      const statistics = current.statistics.snapshot()
      current.models.connect()
      reconnecting = Promise.all([
        current.relay.connect(),
        current.providers.connect(),
        current.activity.connect(),
        current.settings.connect(),
        current.guardrails.connect(),
        current.tunnel?.connect() ?? Promise.resolve(),
        current.clients?.connect() ?? Promise.resolve(),
        current.shared?.connect() ?? Promise.resolve(),
        keys.providerId ? current.apiKeys.load(keys.providerId) : Promise.resolve(),
        routes.phase !== 'idle' ? current.routes.load(routes.target) : Promise.resolve(),
        models.providerId ? current.models.discover(models.providerId) : Promise.resolve(),
        statistics.phase !== 'idle' ? current.statistics.load(statistics.period) : Promise.resolve(),
      ]).then(() => undefined).finally(() => { reconnecting = null })
      return reconnecting
    }
    const shutdown = () => {
      unsubscribeReconnect?.()
      unsubscribeReconnect = null
      services?.relay.dispose()
      services?.providers.dispose()
      services?.activity.dispose()
      services?.apiKeys.dispose()
      services?.settings.dispose()
      services?.guardrails.dispose()
      services?.statistics.dispose()
      services?.routes.dispose()
      services?.tunnel?.dispose()
      services?.clients?.dispose()
      services?.models.dispose()
      services?.shared?.dispose()
      if (session && !sessionStopped) {
        sessionStopped = true
        void session.stop().catch(() => undefined)
      }
    }

    void (async () => {
      try {
        session = await createControlPlaneSession()
        await session.start()
        services = {
          relay: new RelayModel(new StdioRelayPort(session)),
          providers: new ProvidersModel(new StdioProvidersPort(session)),
          activity: new ActivityModel(new StdioActivityPort(session)),
          apiKeys: new ApiKeysModel(new StdioApiKeysPort(session)),
          settings: new SettingsModel(new StdioSettingsPort(session)),
          statistics: new StatisticsModel(new StdioStatisticsPort(session)),
          routes: new RoutesModel(new StdioRoutesPort(session), { tracksBothTargets: __OWNER_EDITION__ }),
          models: new ModelsModel(new StdioModelsPort(session)),
          guardrails: new GuardrailsModel(new StdioGuardrailsPort(session)),
          appVersion: session.appVersion ?? '',
          // Owner-only models are constructed only where their commands exist.
          ...(__OWNER_EDITION__
            ? {
                tunnel: new TunnelModel(new StdioTunnelPort(session)),
                clients: new ClientsModel(new StdioClientsPort(session)),
                shared: new SharedModel(new StdioSharedPort(session)),
              }
            : {}),
        }
        if (disposed) { shutdown(); return }
        setState({ phase: 'ready', services })
        unsubscribeReconnect = session.subscribe('system.reconnected', () => { void connectServices().catch(() => undefined) })
        await connectServices()
        if (disposed) { shutdown(); return }
      } catch {
        if (!disposed) {
          shutdown()
          setState({ phase: 'error', services: null })
        }
      }
    })()

    return () => {
      disposed = true
      shutdown()
    }
  }, [attempt])

  if (state.phase === 'connecting') {
    return <BootstrapView title="Starting Luxury Switchboard" detail="Connecting the local relay…" />
  }
  if (state.phase === 'error') {
    return <BootstrapView title="Luxury Switchboard could not start" detail="The local control plane is unavailable." error onRetry={() => { setState({ phase: 'connecting', services: null }); setAttempt((value) => value + 1) }} />
  }
  return <ServicesContext value={state.services}>{children}</ServicesContext>
}

function BootstrapView({ title, detail, error = false, onRetry }: { title: string; detail: string; error?: boolean; onRetry?: () => void }) {
  return (
    <main className="bootstrap-view" aria-live="polite">
      <div className="brand-mark" aria-hidden="true">S</div>
      <h1>{title}</h1>
      <p className={error ? 'bootstrap-error' : undefined}>{detail}</p>
      {onRetry ? <Button type="button" variant="primary" onClick={onRetry}>Retry</Button> : null}
    </main>
  )
}
