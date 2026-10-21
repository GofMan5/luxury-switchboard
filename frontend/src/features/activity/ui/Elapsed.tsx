import { useEffect, useState } from 'react'
import { formatElapsed } from '../../../shared/format/metrics'
import { elapsedMs, type ActivityRequest } from '../domain/activity'

interface ElapsedProps {
  readonly request: Pick<ActivityRequest, 'startedAt' | 'latencyMs'>
  /** Stops the clock at this instant: the page's "pause visual updates". */
  readonly frozenAt?: number
}

/** How long an in-flight request has been running, ticking on its own
 * whole-second boundary. Only this cell re-renders each second, never the
 * table around it. */
export function Elapsed({ request: { startedAt, latencyMs }, frozenAt }: ElapsedProps) {
  const [now, setNow] = useState(Date.now)
  useEffect(() => {
    if (frozenAt !== undefined) return
    // Resuming from a pause must not show the moment the clock last ticked.
    setNow(Date.now())
    let timer = 0
    const schedule = () => {
      const intoSecond = elapsedMs({ startedAt, latencyMs }, Date.now()) % 1_000
      timer = window.setTimeout(() => {
        setNow(Date.now())
        schedule()
      }, 1_000 - intoSecond)
    }
    schedule()
    return () => window.clearTimeout(timer)
  }, [startedAt, latencyMs, frozenAt])
  return <>{formatElapsed(elapsedMs({ startedAt, latencyMs }, frozenAt ?? now))}</>
}
