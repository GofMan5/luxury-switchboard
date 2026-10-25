import { useCallback, useId, useState, type CSSProperties } from 'react'
import { Button } from '../../../shared/ui/Button'
import { useModalFocus } from '../../../shared/ui/useModalFocus'
import type { PluginDialog } from '../application/plugin-host'
import type { PluginQuestion, PluginsModel } from '../application/plugins-model'
import type { PageEvent } from '../domain/protocol'
import { PluginTree, type FormValues } from './PluginTree'
import { usePlugins } from './usePlugins'
import { useStyleHold } from './useStyleHold'
import styles from './PluginsPage.module.css'

/** Shows the question a plugin is asking (`ui.confirm`), one at a time, over
 * whatever screen is open. Mounted once by the shell. */
export function PluginQuestionSurface() {
  const { model, state } = usePlugins()
  // A question comes first; plugin dialogs wait behind it, one at a time.
  if (state.asking) {
    // Keyed by question so focus and the style hold start over for the next one.
    return <QuestionDialog key={state.asking.id} question={state.asking} onAnswer={(yes) => model.answer(state.asking!.id, yes)} />
  }
  const dialog = Object.values(state.dialogs)[0]
  return dialog ? <PluginDialogView key={`${dialog.pluginId}/${dialog.id}`} dialog={dialog} model={model} running={state.runtimes[dialog.pluginId]?.state === 'running'} /> : null
}

const DIALOG_WIDTH = { small: '420px', medium: '560px', large: '760px' } as const

/** A dialog a plugin opened (`ui.dialog`): its tree and buttons, framed and
 * attributed by the shell. Escape and the close button dismiss it. */
function PluginDialogView({ dialog, model, running }: { readonly dialog: PluginDialog; readonly model: PluginsModel; readonly running: boolean }) {
  const { pluginId, id } = dialog
  const [values] = useState<FormValues>(() => new Map())
  const answer = (action: string | null) => model.answerDialog(pluginId, id, action, Object.fromEntries(values))
  const ref = useModalFocus<HTMLElement>(() => answer(null))
  const labelId = useId()
  useStyleHold()
  const send = useCallback((event: PageEvent) => model.sendSurfaceEvent(pluginId, `dialog:${id}`, event), [model, pluginId, id])
  return (
    <div className="ui-scrim">
      <section ref={ref} className={`ui-modal ${styles.dialog}`} style={{ '--dialog-width': DIALOG_WIDTH[dialog.size] } as CSSProperties} role="dialog" aria-modal="true" aria-labelledby={`${labelId}-title`}>
        <header>
          <div>
            <h2 id={`${labelId}-title`}>{dialog.title}</h2>
            <p>From the plugin <strong className={styles.asker}>{dialog.pluginName}</strong></p>
          </div>
        </header>
        <div className={styles.dialogBody}>
          {dialog.problem ? <p className={styles.dialogError} role="alert">The plugin sent something this window cannot draw: {dialog.problem}</p> : null}
          <PluginTree tree={dialog.tree} disabled={!running} onEvent={send} values={values} />
        </div>
        <footer>
          {dialog.actions.length === 0 ? <Button data-autofocus onClick={() => answer(null)}>Close</Button> : dialog.actions.map((action, index) => (
            <Button key={action.id} data-autofocus={index === 0 ? true : undefined} variant={action.tone === 'neutral' ? undefined : action.tone} disabled={!running} onClick={() => answer(action.id)}>{action.label}</Button>
          ))}
        </footer>
      </section>
    </div>
  )
}

export function QuestionDialog({ question, onAnswer }: { readonly question: PluginQuestion; readonly onAnswer: (yes: boolean) => void }) {
  const ref = useModalFocus<HTMLElement>(() => onAnswer(false))
  const id = useId()
  useStyleHold()
  return (
    <div className="ui-scrim">
      <section ref={ref} className={`ui-modal ${styles.dialog}`} role="alertdialog" aria-modal="true" aria-labelledby={`${id}-title`} aria-describedby={`${id}-body`}>
        <header>
          <div>
            <h2 id={`${id}-title`}>{question.title}</h2>
            {/* The asker is named by the shell, not by the plugin's own text. */}
            <p>Asked by the plugin <strong className={styles.asker}>{question.pluginName}</strong></p>
          </div>
        </header>
        <div className={styles.dialogBody}>
          <p id={`${id}-body`} className={styles.reviewDescription}>{question.body || 'The plugin did not say more.'}</p>
        </div>
        <footer>
          <Button data-autofocus onClick={() => onAnswer(false)}>Cancel</Button>
          <Button variant={question.tone === 'danger' ? 'danger' : 'primary'} onClick={() => onAnswer(true)}>{question.confirmText}</Button>
        </footer>
      </section>
    </div>
  )
}
