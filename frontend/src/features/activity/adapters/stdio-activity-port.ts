import type { ControlPlaneSession } from '../../../platform/stdio/session'
import type { ActivityPort } from '../application/activity-port'
import type { ActivityRequest, ActivitySummary } from '../domain/activity'

export class StdioActivityPort implements ActivityPort {
  readonly #session: ControlPlaneSession

  constructor(session: ControlPlaneSession) {
    this.#session = session
  }

  async list(limit: number, signal?: AbortSignal): Promise<readonly ActivityRequest[]> {
    const result = await this.#session.call<{ requests: readonly ActivityRequest[] }>(
      'activity.list',
      { limit },
      signal,
    )
    return result.requests
  }

  summary(signal?: AbortSignal): Promise<ActivitySummary> {
    return this.#session.call('activity.summary', undefined, signal)
  }

  subscribe(listener: (request: ActivityRequest) => void): () => void {
    return this.#session.subscribe<ActivityRequest>('activity.changed', (event) => {
      if (event.payload) listener(event.payload)
    })
  }
}
