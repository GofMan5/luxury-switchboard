import type { ControlPlaneSession } from '../../../platform/stdio/session'
import type { ControlPort, PluginsPort } from '../application/ports'
import type { PluginPackage } from '../domain/manifest'
import { normalizeDetail, normalizeList, normalizeSummary, normalizeValues, strings, type PluginDetail, type PluginSummary } from '../domain/plugin'

export class StdioPluginsPort implements PluginsPort {
  readonly #session: ControlPlaneSession

  constructor(session: ControlPlaneSession) { this.#session = session }

  async list(signal?: AbortSignal): Promise<PluginSummary[]> {
    return normalizeList(await this.#session.call('plugins.list', {}, signal))
  }

  async get(id: string, signal?: AbortSignal): Promise<PluginDetail> {
    return normalizeDetail(await this.#session.call('plugins.get', { id }, signal))
  }

  async install(pkg: PluginPackage, granted: readonly string[], source?: string): Promise<PluginSummary> {
    const answer = await this.#session.call<{ plugin?: unknown }>('plugins.install', { package: pkg, granted, ...(source ? { source } : {}) })
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

  async storageUpdate(id: string, values: Readonly<Record<string, string | null>>): Promise<Record<string, string>> {
    return normalizeValues(await this.#session.call('plugins.storage.update', { id, values }))
  }

  async secretSet(id: string, key: string, value: string | null): Promise<string[]> {
    const answer = await this.#session.call<{ secrets?: unknown }>('plugins.secret.set', { id, key, value })
    return strings(answer?.secrets)
  }

  async devLink(path: string): Promise<PluginSummary> {
    return summaryOf(await this.#session.call('plugins.dev.link', { path }))
  }

  async devUnlink(id: string): Promise<PluginSummary> {
    return summaryOf(await this.#session.call('plugins.dev.unlink', { id }))
  }

  async download(url: string): Promise<{ readonly package: unknown; readonly source: string }> {
    const answer = await this.#session.call<{ package?: unknown; source?: unknown }>('plugins.download', { url })
    return { package: answer?.package, source: typeof answer?.source === 'string' ? answer.source : '' }
  }

  async export(id: string): Promise<unknown> {
    const answer = await this.#session.call<{ package?: unknown }>('plugins.export', { id })
    return answer?.package
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

/** `{ plugin }` as the other commands answer, or the summary itself. */
function summaryOf(answer: unknown): PluginSummary {
  const wrapped = answer && typeof answer === 'object' && 'plugin' in answer ? (answer as { plugin: unknown }).plugin : answer
  return normalizeSummary(wrapped)
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
