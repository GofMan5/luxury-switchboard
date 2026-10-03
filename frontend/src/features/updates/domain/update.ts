export interface UpdateCheck {
  readonly current: string
  readonly latest: string
  readonly url: string
  readonly newer: boolean
  readonly reachable: boolean
  readonly checkedAt: string
}
