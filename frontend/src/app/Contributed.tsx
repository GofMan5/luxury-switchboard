import { Component, useState, type ErrorInfo, type KeyboardEvent, type PropsWithChildren, type ReactNode } from 'react'
import type { ActionContribution, SlotContext, SlotContribution, SlotScope } from '../shared/contracts/extensions'
import { Button } from '../shared/ui/Button'
import { pluginIcon } from './plugin-icons'
import { actionName, useContributed } from './contributions'
import styles from './Contributed.module.css'

/**
 * A contributed slot in its frame: the title, "From <owner>", and an error
 * boundary, so one plugin's failure stays inside its own frame. `bare` drops
 * the frame chrome for a host that already shows the title (an inspector tab).
 */
export function SlotFrame({ slot, context, bare = false }: { readonly slot: SlotContribution; readonly context: SlotContext; readonly bare?: boolean }) {
  const body = <SlotBoundary key={`${slot.id}:${context.id ?? ''}`}><SlotBody slot={slot} context={context} /></SlotBoundary>
  if (bare) {
    return (
      <div className={styles.bare} data-ui="slot" data-slot={slot.id}>
        <p className={styles.owner}>From {slot.owner}</p>
        {body}
      </div>
    )
  }
  return (
    <section className={styles.frame} aria-label={`${slot.title}, from ${slot.owner}`} data-ui="slot" data-slot={slot.id} data-wide={slot.wide || undefined}>
      <header className={styles.header}>
        <h3 className={styles.title}>{slot.title}</h3>
        <span className={styles.owner}>From {slot.owner}</span>
      </header>
      {slot.description ? <p className={styles.description}>{slot.description}</p> : null}
      <div className={styles.body}>{body}</div>
    </section>
  )
}

function SlotBody({ slot, context }: { readonly slot: SlotContribution; readonly context: SlotContext }) {
  return <>{slot.render(context)}</>
}

/** Overview widgets: a grid that is absent, not empty, when nothing is contributed. */
export function SlotGrid({ scope }: { readonly scope: SlotScope }) {
  const { slots } = useContributed(scope)
  if (slots.length === 0) return null
  return (
    <div className={styles.grid} data-ui="slot-grid" data-scope={scope}>
      {slots.map((slot) => <SlotFrame key={slot.id} slot={slot} context={{ scope }} />)}
    </div>
  )
}

/** Contributed buttons for one record; nothing at all when there are none. */
export function ActionButtons({ actions, targetId, compact = false }: { readonly actions: readonly ActionContribution[]; readonly targetId: string; readonly compact?: boolean }) {
  if (actions.length === 0) return null
  return (
    <>
      {actions.map((action) => {
        const Icon = pluginIcon(action.icon)
        const name = actionName(action)
        return (
          <Button
            key={action.id}
            type="button"
            variant={action.tone === 'danger' ? 'danger' : compact ? 'ghost' : 'secondary'}
            data-ui="contributed-action"
            aria-label={compact ? name : undefined}
            title={name}
            onClick={() => action.run(targetId)}
          >
            <Icon size={13} strokeWidth={1.8} />
            {compact ? null : action.title}
          </Button>
        )
      })}
    </>
  )
}

interface BoundaryState {
  readonly failed: boolean
}

/** Neutral by design: the error text is the plugin's, and the app does not repeat it. */
class SlotBoundary extends Component<PropsWithChildren, BoundaryState> {
  state: BoundaryState = { failed: false }

  static getDerivedStateFromError(): BoundaryState {
    return { failed: true }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('plugin slot render failed', error, info.componentStack)
  }

  render(): ReactNode {
    if (!this.state.failed) return this.props.children
    return (
      <div className={styles.failed} role="status" data-ui="slot-error">
        <span>This section could not be displayed.</span>
        <Button type="button" variant="ghost" onClick={() => this.setState({ failed: false })}>Try again</Button>
      </div>
    )
  }
}

/**
 * An inspector's own content under a "Details" tab, plus one tab per
 * contributed slot. With no slots there is no tab strip at all: the
 * inspector looks exactly as it did before plugins existed.
 */
export function InspectorTabs({ scope, id, label, children }: PropsWithChildren<{ readonly scope: 'activity' | 'provider'; readonly id: string; readonly label: string }>) {
  const { slots } = useContributed(scope)
  const [chosen, setChosen] = useState('details')
  const tab = chosen === 'details' || slots.some((slot) => slot.id === chosen) ? chosen : 'details'
  if (slots.length === 0) return <>{children}</>
  const tabs = [{ id: 'details', title: 'Details' }, ...slots.map((slot) => ({ id: slot.id, title: slot.title }))]
  const slot = slots.find((entry) => entry.id === tab)
  const domId = (value: string) => `${scope}-inspector-${value.replace(/[^a-z0-9_-]/giu, '-')}`
  const keys = (event: KeyboardEvent<HTMLDivElement>) => {
    const step = event.key === 'ArrowRight' ? 1 : event.key === 'ArrowLeft' ? -1 : 0
    const at = tabs.findIndex((entry) => entry.id === tab)
    const next = step ? tabs[(at + step + tabs.length) % tabs.length] : event.key === 'Home' ? tabs[0] : event.key === 'End' ? tabs.at(-1) : undefined
    if (!next) return
    event.preventDefault()
    setChosen(next.id)
    event.currentTarget.querySelector<HTMLElement>(`#${domId(next.id)}-tab`)?.focus()
  }
  return (
    <>
      <div className={styles.tabs} role="tablist" aria-label={label} onKeyDown={keys} data-ui="inspector-tabs">
        {tabs.map((entry) => (
          <button
            key={entry.id}
            type="button"
            role="tab"
            id={`${domId(entry.id)}-tab`}
            aria-selected={entry.id === tab}
            aria-controls={`${domId(entry.id)}-panel`}
            tabIndex={entry.id === tab ? 0 : -1}
            className={styles.tab}
            onClick={() => setChosen(entry.id)}
          >
            {entry.title}
          </button>
        ))}
      </div>
      <div role="tabpanel" id={`${domId(tab)}-panel`} aria-labelledby={`${domId(tab)}-tab`} className={styles.tabPanel}>
        {slot ? <SlotFrame slot={slot} context={{ scope, id }} bare /> : children}
      </div>
    </>
  )
}
