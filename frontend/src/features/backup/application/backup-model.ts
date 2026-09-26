import type { BackupPort, BackupReport } from '../adapters/stdio-backup-port'

export interface BackupState {
  readonly exporting: boolean
  readonly importing: boolean
  /** Where the last export landed, kept so the notice can offer it again. */
  readonly lastExportPath: string
  /** The last import's outcome, kept until the next action. */
  readonly lastReport: BackupReport | null
  readonly error: string
}

export class BackupModel {
  readonly #port: BackupPort
  #state: BackupState = { exporting: false, importing: false, lastExportPath: '', lastReport: null, error: '' }
  #listeners = new Set<() => void>()

  constructor(port: BackupPort) {
    this.#port = port
  }

  snapshot = (): BackupState => this.#state

  subscribe = (listener: () => void): (() => void) => {
    this.#listeners.add(listener)
    return () => this.#listeners.delete(listener)
  }

  async export(): Promise<string> {
    if (this.#state.exporting) return ''
    this.#set({ ...this.#state, exporting: true, error: '' })
    try {
      const { path } = await this.#port.export()
      this.#set({ ...this.#state, exporting: false, lastExportPath: path, lastReport: null })
      return path
    } catch {
      this.#set({ ...this.#state, exporting: false, error: 'The backup could not be written' })
      return ''
    }
  }

  async import(content: string): Promise<BackupReport | null> {
    if (this.#state.importing || !content.trim()) return null
    this.#set({ ...this.#state, importing: true, error: '' })
    try {
      const report = await this.#port.import(content)
      this.#set({ ...this.#state, importing: false, lastReport: report })
      return report
    } catch {
      this.#set({ ...this.#state, importing: false, error: 'The file could not be restored' })
      return null
    }
  }

  clearError(): void {
    if (this.#state.error) this.#set({ ...this.#state, error: '' })
  }

  #set(state: BackupState): void {
    this.#state = state
    for (const listener of this.#listeners) listener()
  }
}
