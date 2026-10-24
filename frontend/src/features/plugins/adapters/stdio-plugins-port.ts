import type { ControlPlaneSession } from '../../../platform/stdio/session'
import type { ControlPort, PluginsPort } from '../application/ports'
import type { PluginPackage } from '../domain/manifest'
import { normalizeDetail, normalizeList, normalizeSummary, normalizeValues, type PluginDetail, type PluginSummary } from '../domain/plugin'

export class StdioPluginsPort implements PluginsPort {
  readonly #session: ControlPlaneSession

  constructor(session: ControlPlaneSession) { this.#session = session }

  async list(signal?: AbortSignal): Promise<PluginSummary[]> {
    return normalizeList(await this.#session.call('plugins.list', {}, signal))
  }

  async get(id: string, signal?: AbortSignal): Promise<PluginDetail> {
    return normalizeDetail(await this.#session.call('plugins.get', { id }, signal))
  }

  async install(pkg: PluginPackage, granted: readonly string[]): Promise<PluginSummary> {
    const answer = await this.#session.call<{ plugin?: unknown }>('plugins.install', { package: pkg, granted })
    return normalizeSummary(answer?.plugin)
  }

  async update(id: string, change: { readonly enabled?: boolean; readonly granted?: readonly string[] }): Promise<PluginSummary> {
    const answer = await this.#session.call<{ plugin?: unknown }>('plugins.update', { id, ...change })
    return normalizeSummary(answer?.plugin)
  }

  async remove(id: string): Promise<void> {
    await this.#session.call('plugins.remove', { id })
  }

  async storageGet(id: string): Promise<Record<string, string>> {
    return normalizeValues(await this.#session.call('plugins.storage.get', { id }))
  }

  async storageSet(id: string, key: string, value: string | null): Promise<Record<string, string>> {
    return normalizeValues(await this.#session.call('plugins.storage.set', { id, key, value }))
  }

  subscribe(listener: (plugins: PluginSummary[]) => void): () => void {
    return this.#session.subscribe('plugins.changed', (event) => {
      try {
        listener(normalizeList(event.payload))
      } catch {
        // A malformed event is ignored; the next refresh corrects the list.
      }
    })
  }
}

/** The control plane as plugins see it. The host has already checked the
 * method and topic against the plugin's grants; this only carries them. */
export class SessionControlPort implements ControlPort {
  readonly #session: ControlPlaneSession

  constructor(session: ControlPlaneSession) { this.#session = session }

  call(method: string, payload: unknown, signal?: AbortSignal): Promise<unknown> {
    return this.#session.call(method, payload, signal)
  }

  subscribe(topic: string, listener: (payload: unknown) => void): () => void {
    return this.#session.subscribe(topic, (event) => listener(event.payload))
  }
}
