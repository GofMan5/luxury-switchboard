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
import { ServicesContext, type AppServices } from './services'

type BootstrapState =
  | { readonly phase: 'connecting'; readonly services: null }
  | { readonly phase: 'ready'; readonly services: AppServices }
  | { readonly phase: 'error'; readonly services: null }

export function ServicesProvider({ children }: PropsWithChildren) {
  const [state, setState] = useState<BootstrapState>({ phase: 'connecting', services: null })

  useEffect(() => {
    let disposed = false
    let session: ControlPlaneSession | null = null
    let services: AppServices | null = null

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
          routes: new RoutesModel(new StdioRoutesPort(session)),
          tunnel: new TunnelModel(new StdioTunnelPort(session)),
          clients: new ClientsModel(new StdioClientsPort(session)),
          models: new ModelsModel(new StdioModelsPort(session)),
          shared: new SharedModel(new StdioSharedPort(session)),
        }
        if (disposed) return
        setState({ phase: 'ready', services })
        await Promise.all([
          services.relay.connect(),
          services.providers.connect(),
          services.activity.connect(),
          services.settings.connect(),
          services.tunnel.connect(),
          services.clients.connect(),
          services.shared.connect(),
        ])
        services.models.connect()
      } catch {
        if (!disposed) setState({ phase: 'error', services: null })
      }
    })()

    return () => {
      disposed = true
      services?.relay.dispose()
      services?.providers.dispose()
      services?.activity.dispose()
      services?.apiKeys.dispose()
      services?.settings.dispose()
      services?.statistics.dispose()
      services?.routes.dispose()
      services?.tunnel.dispose()
      services?.clients.dispose()
      services?.models.dispose()
      services?.shared.dispose()
      if (session) void session.stop()
    }
  }, [])

  if (state.phase === 'connecting') {
    return <BootstrapView title="Starting Switchboard" detail="Connecting the local relay…" />
  }
  if (state.phase === 'error') {
    return <BootstrapView title="Switchboard could not start" detail="The local control plane is unavailable." error />
  }
  return <ServicesContext value={state.services}>{children}</ServicesContext>
}

function BootstrapView({ title, detail, error = false }: { title: string; detail: string; error?: boolean }) {
  return (
    <main className="bootstrap-view" aria-live="polite">
      <div className="brand-mark" aria-hidden="true">S</div>
      <h1>{title}</h1>
      <p className={error ? 'bootstrap-error' : undefined}>{detail}</p>
    </main>
  )
}
