export interface UpdateCheck {
  readonly current: string
  readonly latest: string
  readonly url: string
  readonly newer: boolean
  readonly reachable: boolean
  readonly checkedAt: string
}

/** One progress report of an in-flight installer download. */
export interface UpdateInstallProgress {
  readonly phase: 'downloading' | 'verifying' | 'ready'
  readonly received: number
  readonly total: number
  readonly percent: number
}

/** Where a verified installer waits for the shell to run it. */
export interface UpdateInstall {
  readonly path: string
  readonly version: string
}
