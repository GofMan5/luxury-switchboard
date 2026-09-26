import type { ControlPlaneSession } from '../../../platform/stdio/session'

export interface BackupReport {
  readonly providersAdded: number
  readonly keysAdded: number
  readonly routesAdded: number
  readonly providersSkipped: number
  readonly keysSkipped: number
  readonly routesSkipped: number
  readonly failed: number
}

export interface BackupPort {
  export(signal?: AbortSignal): Promise<{ path: string }>
  import(content: string, signal?: AbortSignal): Promise<BackupReport>
}

export class StdioBackupPort implements BackupPort {
  readonly #session: ControlPlaneSession

  constructor(session: ControlPlaneSession) {
    this.#session = session
  }

  export(signal?: AbortSignal): Promise<{ path: string }> {
    return this.#session.call('backup.export', undefined, signal)
  }

  import(content: string, signal?: AbortSignal): Promise<BackupReport> {
    return this.#session.call('backup.import', { content }, signal)
  }
}
