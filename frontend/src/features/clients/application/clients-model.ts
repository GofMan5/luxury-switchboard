import type { TunnelClient, TunnelClientEvent, TunnelClientProfile } from '../domain/client'
import { MAX_CLIENT_NOTE } from '../domain/client'
import type { ClientsPort } from './clients-port'

export interface ClientsState {
  readonly phase: 'loading' | 'ready' | 'error'
  readonly clients: readonly TunnelClient[]
  readonly events: readonly TunnelClientEvent[]
  readonly selectedIp: string
  readonly pendingIp: string
  readonly error: string
}

export class ClientsModel {
  readonly #port: ClientsPort
  #state: ClientsState = { phase: 'loading', clients: [], events: [], selectedIp: '', pendingIp: '', error: '' }
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
      if (this.#timer !== undefined) return
      this.#timer = setTimeout(() => {
        this.#timer = undefined
        void this.refresh()
      }, 200)
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
    this.#set({ ...this.#state, selectedIp: ip, events: ip === this.#state.selectedIp ? this.#state.events : [], error: '' })
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

  /** Bans, unbans or annotates one address. Bans only affect new requests. */
  async saveProfile(profile: TunnelClientProfile): Promise<boolean> {
    if (this.#state.pendingIp) return false
    const note = profile.note.trim()
    if (!profile.ip || note.length > MAX_CLIENT_NOTE) {
      this.#set({ ...this.#state, error: `A client note is limited to ${MAX_CLIENT_NOTE} characters` })
      return false
    }
    this.#set({ ...this.#state, pendingIp: profile.ip, error: '' })
    try {
      const clients = await this.#port.saveProfile({ ...profile, note })
      this.#set({ ...this.#state, phase: 'ready', clients, pendingIp: '', error: '' })
      return true
    } catch {
      this.#set({ ...this.#state, pendingIp: '', error: 'Client decision could not be saved' })
      return false
    }
  }

  clearError() {
    if (this.#state.error) this.#set({ ...this.#state, error: '' })
  }

  dispose() {
    this.#unsubscribe?.()
    clearTimeout(this.#timer)
    this.#timer = undefined
    this.#listeners.clear()
  }

  #set(state: ClientsState) {
    this.#state = state
    for (const listener of this.#listeners) listener()
  }
}
