import { useState } from 'react'
import { AlertTriangle, Bell, CheckCircle2, Info, Trash2, X } from 'lucide-react'
import { Button } from '../../../shared/ui/Button'
import { useModalFocus } from '../../../shared/ui/useModalFocus'
import { useNotifications } from './useNotifications'
import type { Notification } from '../adapters/stdio-notifications-port'
import styles from './Notifications.module.css'

const severityIcon = {
  info: Info,
  success: CheckCircle2,
  warning: AlertTriangle,
  danger: AlertTriangle,
} as const

/** The toast stack and the bell live at the shell level: any workspace can
 * raise an event, and the operator sees it without navigating anywhere. */
export function NotificationsSurface() {
  const { state, model } = useNotifications()
  const [panelOpen, setPanelOpen] = useState(false)
  const toasts = state.enabled ? model.toasts() : []
  return (
    <>
      <button
        type="button"
        className={styles.bell}
        aria-label={`Notifications${state.unread > 0 ? `, ${state.unread} unread` : ''}`}
        onClick={() => {
          setPanelOpen((open) => !open)
          if (!panelOpen) model.markRead()
        }}
      >
        <Bell size={16} aria-hidden="true" />
        {state.unread > 0 ? <span className={styles.badge} aria-hidden="true">{state.unread > 9 ? '9+' : state.unread}</span> : null}
      </button>
      {panelOpen ? <NotificationsPanel onClose={() => setPanelOpen(false)} /> : null}
      {toasts.length > 0 ? (
        <div className={styles.toasts} role="region" aria-label="Notifications" aria-live="polite">
          {toasts.map((notification) => <Toast key={notification.id} notification={notification} />)}
        </div>
      ) : null}
    </>
  )
}

function Toast({ notification }: { notification: Notification }) {
  const Icon = severityIcon[notification.severity] ?? Info
  return (
    <div className={styles.toast} data-severity={notification.severity}>
      <Icon size={15} aria-hidden="true" data-severity={notification.severity} />
      <div className={styles.toastBody}>
        <strong>{notification.title}</strong>
        <p>{notification.body}</p>
      </div>
    </div>
  )
}

function NotificationsPanel({ onClose }: { onClose: () => void }) {
  const { state, model } = useNotifications()
  const dialogRef = useModalFocus<HTMLElement>(onClose)
  // The panel is anchored to the bell, not centered: it is a glance surface.
  return (
    <section
      ref={dialogRef}
      className={styles.panel}
      role="dialog"
      aria-modal="true"
      aria-label="Notifications"
    >
      <header>
        <h2>Notifications</h2>
        <Button variant="ghost" disabled={state.notifications.length === 0} onClick={() => void model.clear()}>
          <Trash2 size={14} aria-hidden="true" />
          Clear
        </Button>
        <button type="button" className={styles.close} aria-label="Close notifications" onClick={onClose}>
          <X size={16} aria-hidden="true" />
        </button>
      </header>
      <div className={styles.panelBody}>
        {state.phase === 'loading' ? <p className={styles.empty}>Loading…</p> : null}
        {state.phase === 'error' ? <p className={styles.empty}>{state.error}</p> : null}
        {state.phase === 'ready' && state.notifications.length === 0 ? (
          <p className={styles.empty}>Nothing yet. Failovers, dead keys and unreachable providers land here.</p>
        ) : null}
        {state.notifications.map((notification) => (
          <PanelRow key={notification.id} notification={notification} />
        ))}
      </div>
    </section>
  )
}

function PanelRow({ notification }: { notification: Notification }) {
  const Icon = severityIcon[notification.severity] ?? Info
  return (
    <article className={styles.row} data-severity={notification.severity}>
      <Icon size={15} aria-hidden="true" data-severity={notification.severity} />
      <div>
        <strong>{notification.title}</strong>
        <p>{notification.body}</p>
        <time>{formatClock(notification.at)}</time>
      </div>
    </article>
  )
}

function formatClock(value: string): string {
  const parsed = Date.parse(value)
  if (Number.isNaN(parsed)) return ''
  return new Date(parsed).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}
