import { Component, type ErrorInfo, type PropsWithChildren, type ReactNode } from 'react'
import { RotateCcw } from 'lucide-react'
import { Button } from './Button'
import styles from './WorkspaceBoundary.module.css'

interface WorkspaceBoundaryState {
  readonly failed: boolean
}

/**
 * Keeps one bad screen from taking the window with it.
 *
 * There was no boundary anywhere, and React unmounts the whole tree on a render
 * throw: one malformed record — a provider answer missing a field a cell reads —
 * left an empty window with no navigation, so the only way out was to close the
 * app. This wraps the workspace rather than the shell on purpose: the sidebar has
 * to survive, because leaving the broken screen is the recovery.
 *
 * Navigating is the other half of that recovery, and the caller keys this on the
 * route so React discards the failed instance instead of the class reaching for
 * the previous props to work it out for itself.
 *
 * It states that the screen failed and nothing more. The message would be the
 * provider's or the record's, and this app is careful about what it repeats.
 */
export class WorkspaceBoundary extends Component<PropsWithChildren, WorkspaceBoundaryState> {
  state: WorkspaceBoundaryState = { failed: false }

  static getDerivedStateFromError(): WorkspaceBoundaryState {
    return { failed: true }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // The console is the owner's own machine; the UI stays quiet about details.
    console.error('workspace render failed', error, info.componentStack)
  }

  render(): ReactNode {
    if (!this.state.failed) return this.props.children
    return (
      <div className={styles.failure} role="alert">
        <h2>This screen could not be displayed</h2>
        <p>Something in the data it received was not what it expected. Every other screen still works.</p>
        <Button type="button" onClick={() => this.setState({ failed: false })}>
          <RotateCcw size={14} aria-hidden="true" />
          Try again
        </Button>
      </div>
    )
  }
}
