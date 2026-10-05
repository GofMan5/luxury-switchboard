import { useEffect, useState } from 'react'
import { ArrowUpCircle, ShieldCheck } from 'lucide-react'
import type { UpdateCheck } from '../domain/update'
import type { UpdatesState } from '../application/updates-model'
import { Button } from '../../../shared/ui/Button'
import { useModalFocus } from '../../../shared/ui/useModalFocus'
import { openExternal } from '../../../platform/lifecycle/open-external'
import { runInstaller } from '../../../platform/lifecycle/run-installer'
import styles from './UpdateDialog.module.css'

/** The one update surface: what's out, what the download is doing, and the
 * only two honest exits (install it yourself, or read the release page). */
export function UpdateDialog({ check, state, onClose, onInstall }: {
  readonly check: UpdateCheck
  readonly state: UpdatesState
  readonly onClose: () => void
  readonly onInstall: () => Promise<boolean>
}) {
  const busy = state.installPhase === 'downloading' || state.installPhase === 'verifying'
  const dialogRef = useModalFocus<HTMLDivElement>(onClose, busy)
  const [launchError, setLaunchError] = useState('')
  const [launchNamesThePage, setLaunchNamesThePage] = useState(false)
  const [launching, setLaunching] = useState(false)

  // The phase swap unmounts the focused button, which would drop focus to the
  // body and kill Escape behind the scrim: every phase change hands focus back
  // to the dialog itself, where the keydown trap lives.
  useEffect(() => {
    dialogRef.current?.focus()
  }, [state.installPhase, dialogRef])

  // A platform the release ships no installer for is not a transient failure:
  // no amount of retrying changes what the release contains, so "Try again"
  // goes away and the release page becomes the action worth taking.
  const noSelfUpdate = state.installPhase === 'error' && state.installError.includes('no self-update')

  const launch = async () => {
    setLaunchError('')
    setLaunchNamesThePage(false)
    setLaunching(true)
    try {
      await runInstaller(state.installerPath, true)
    } catch (error) {
      // The shell refused or is absent (dev fixture in a browser). The refusal
      // carries its own reason; the Rust command rejects with plain strings,
      // not Error, so both shapes are read. Most refusals leave the file
      // verified and on disk, so the operator can run it by hand — except the
      // page-only ones (a deb install on Linux, a bundle-less one on macOS):
      // the reason itself already names the release page as the path, and a
      // restart would just refuse again, so the file line and the dead
      // restart button go away and the release page becomes the action.
      const reason = error instanceof Error ? error.message : String(error)
      const namesThePage = reason.includes('the release page is the path')
      setLaunchNamesThePage(namesThePage)
      setLaunchError(
        namesThePage
          ? `The shell could not start the installer: ${reason}`
          : `The shell could not start the installer: ${reason}. It is verified and on disk:`
      )
    } finally {
      setLaunching(false)
    }
  }

  return (
    <div className="ui-scrim" role="presentation" onClick={busy ? undefined : onClose}>
      <section ref={dialogRef} tabIndex={-1} className={`ui-modal ${styles.dialog}`} role="dialog" aria-modal="true" aria-labelledby="update-title" onClick={(event) => event.stopPropagation()}>
        <header>
          <div>
            <h2 id="update-title">Version {check.latest} is out</h2>
            <p>You are running {check.current}. The download is verified against the release's own checksum before anything runs.</p>
          </div>
          <ArrowUpCircle size={22} aria-hidden="true" />
        </header>

        {state.installPhase === 'idle' ? (
          <div className={styles.actions}>
            <Button variant="primary" onClick={() => void onInstall()}><ArrowUpCircle size={15} />Download and verify</Button>
            <Button onClick={() => void openExternal(check.url)}>Release page</Button>
          </div>
        ) : null}

        {busy ? (
          <div className={styles.progress} role="status">
            <div className={styles.track}><div className={styles.fill} style={{ width: `${state.installPercent}%` }} /></div>
            <span>{state.installPhase === 'downloading' ? `${state.installPercent}%` : 'Verifying checksum…'}</span>
          </div>
        ) : null}

        {state.installPhase === 'ready' ? (
          <div className={styles.ready}>
            <div><ShieldCheck size={16} aria-hidden="true" /><span>Verified. The installer is ready.</span></div>
            <div className={styles.actions}>
              {launchNamesThePage ? null : (
                <Button variant="primary" disabled={launching} onClick={() => void launch()}>{launching ? 'Starting…' : 'Restart and install'}</Button>
              )}
              <Button variant={launchNamesThePage ? 'primary' : 'secondary'} disabled={launching} onClick={() => void openExternal(check.url)}>Release page</Button>
            </div>
            {launchError ? <p className={styles.launchError} role="alert">{launchError}</p> : null}
            {launchError && !launchNamesThePage ? <code className={styles.path}>{state.installerPath}</code> : null}
          </div>
        ) : null}

        {state.installPhase === 'error' ? (
          <div className={styles.error} role="alert">
            <p>{state.installError || 'The update could not be downloaded.'}</p>
            <div className={styles.actions}>
              {noSelfUpdate ? null : (
                <Button variant="primary" onClick={() => void onInstall()}>Try again</Button>
              )}
              <Button variant={noSelfUpdate ? 'primary' : 'secondary'} onClick={() => void openExternal(check.url)}>Release page</Button>
            </div>
          </div>
        ) : null}

        <footer>
          <button type="button" className={styles.close} onClick={onClose} disabled={busy}>Not now</button>
        </footer>
      </section>
    </div>
  )
}
