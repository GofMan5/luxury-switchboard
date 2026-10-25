/**
 * The window's message bus between running plugins. Each host subscribes on
 * its plugin's behalf; a publish reaches every other plugin on the channel,
 * never the sender. Payloads arrive already checked and freshly parsed, so no
 * two plugins share an object.
 */
export class PluginBus {
  #channels = new Map<string, Map<string, (payload: unknown, from: string) => void>>()

  subscribe(pluginId: string, channel: string, deliver: (payload: unknown, from: string) => void): () => void {
    let listeners = this.#channels.get(channel)
    if (!listeners) {
      listeners = new Map()
      this.#channels.set(channel, listeners)
    }
    listeners.set(pluginId, deliver)
    return () => {
      const current = this.#channels.get(channel)
      if (current?.get(pluginId) !== deliver) return
      current.delete(pluginId)
      if (current.size === 0) this.#channels.delete(channel)
    }
  }

  publish(from: string, channel: string, payload: unknown): void {
    for (const [pluginId, deliver] of this.#channels.get(channel) ?? []) {
      if (pluginId !== from) deliver(payload, from)
    }
  }
}
