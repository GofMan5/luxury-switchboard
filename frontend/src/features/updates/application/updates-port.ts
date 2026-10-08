import type { UpdateCheck, UpdateInstall, UpdateInstallProgress } from '../domain/update'

/** The update scenario's own port: what the model needs from whatever carries
 * the answers — the framed stdio session in production, a stub in tests. */
export interface UpdatesPort {
  /** A real round trip to the releases feed; the backend single-flights this,
   * so a manual check while the refresher is mid-ask joins it instead of
   * sending a second request. */
  check(signal?: AbortSignal): Promise<UpdateCheck | null>
  /** The last completed answer, straight from the refresher's cache: no round
   * trip, no rate-limit budget, the state the backend already knows. */
  status(signal?: AbortSignal): Promise<UpdateCheck | null>
  install(signal?: AbortSignal): Promise<UpdateInstall>
  onInstallProgress(listener: (report: UpdateInstallProgress) => void): () => void
  /** The refresher's verdict flips arrive here: the pill that says "update
   * available" turns on the minute it becomes true, not at the next command
   * the interface happens to send. */
  onStateChanged(listener: (check: UpdateCheck) => void): () => void
}
