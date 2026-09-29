import { createContext, useContext } from 'react'
import type { ActivityModel } from '../features/activity/application/activity-model'
import type { ApiKeysModel } from '../features/api-keys/application/api-keys-model'
import type { ProvidersModel } from '../features/providers/application/providers-model'
import type { RelayModel } from '../features/relay/application/relay-model'
import type { SettingsModel } from '../features/settings/application/settings-model'
import type { InsightsModel } from '../features/insights/application/insights-model'
import type { RoutesModel } from '../features/model-routes/application/routes-model'
import type { GuardrailsModel } from '../features/guardrails/application/guardrails-model'
import type { TunnelModel } from '../features/tunnel/application/tunnel-model'
import type { ClientsModel } from '../features/clients/application/clients-model'
import type { ModelsModel } from '../features/models/application/models-model'
import type { SharedModel } from '../features/shared-control/application/shared-model'
import type { NotificationsModel } from '../features/notifications/application/notifications-model'
import type { BackupModel } from '../features/backup/application/backup-model'

export interface AppServices {
  readonly relay: RelayModel
  readonly providers: ProvidersModel
  readonly activity: ActivityModel
  readonly apiKeys: ApiKeysModel
  readonly settings: SettingsModel
  readonly insights: InsightsModel
  readonly routes: RoutesModel
  readonly models: ModelsModel
  readonly guardrails: GuardrailsModel
  readonly notifications: NotificationsModel
  readonly backup: BackupModel
  /** Publishing workspaces exist only in the owner edition. */
  readonly tunnel?: TunnelModel
  readonly clients?: ClientsModel
  readonly shared?: SharedModel
  /** Version the control plane reported during the handshake. */
  readonly appVersion: string
}

export const ServicesContext = createContext<AppServices | null>(null)

export function useAppServices(): AppServices {
  const services = useContext(ServicesContext)
  if (!services) throw new Error('App services are not ready')
  return services
}
