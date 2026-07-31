import type { TunnelClient, TunnelClientEvent } from '../domain/client'
import type { ClientsPort } from './clients-port'

export interface ClientsState {
  readonly phase: 'loading' | 'ready' | 'error'
  readonly clients: readonly TunnelClient[]
  readonly events: readonly TunnelClientEvent[]
  readonly selectedIp: string
  readonly error: string
}

export class ClientsModel {
  readonly #port: ClientsPort
  #state: ClientsState = { phase: 'loading', clients: [], events: [], selectedIp: '', error: '' }
  #listeners = new Set<() => void>()
  #unsubscribe: (() => void) | null = null
  #timer: ReturnType<typeof setTimeout> | undefined
  #generation = 0

  constructor(port: ClientsPort) {
    this.#port = port
  }

  snapshot = () => this.#state

  subscribe = (listener: () => void) => {
    this.#listeners.add(listener)
    return () => this.#listeners.delete(listener)
  }

  async connect() {
    this.#unsubscribe ??= this.#port.subscribe(() => {
      clearTimeout(this.#timer)
      this.#timer = setTimeout(() => void this.refresh(), 200)
    })
    await this.refresh()
  }

  async refresh() {
    const generation = ++this.#generation
    try {
      const selectedIp = this.#state.selectedIp
      const [clients, events] = await Promise.all([
        this.#port.list(),
        selectedIp ? this.#port.events(selectedIp) : Promise.resolve(this.#state.events),
      ])
      if (generation === this.#generation) this.#set({ ...this.#state, phase: 'ready', clients, events, error: '' })
    } catch {
      if (generation === this.#generation) this.#set({ ...this.#state, phase: 'error', error: 'Tunnel clients are unavailable' })
    }
  }

  async select(ip: string) {
    const generation = ++this.#generation
    try {
      const events = await this.#port.events(ip)
      if (generation === this.#generation) this.#set({ ...this.#state, selectedIp: ip, events, error: '' })
    } catch {
      if (generation === this.#generation) this.#set({ ...this.#state, error: 'Client events are unavailable' })
    }
  }

  close() {
    this.#generation++
    this.#set({ ...this.#state, selectedIp: '', events: [] })
  }

  dispose() {
    this.#unsubscribe?.()
    clearTimeout(this.#timer)
    this.#listeners.clear()
  }

  #set(state: ClientsState) {
    this.#state = state
    for (const listener of this.#listeners) listener()
  }
}
