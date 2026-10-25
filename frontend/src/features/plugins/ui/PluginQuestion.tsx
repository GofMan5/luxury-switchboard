import { useId } from 'react'
import { Button } from '../../../shared/ui/Button'
import { useModalFocus } from '../../../shared/ui/useModalFocus'
import type { PluginQuestion } from '../application/plugins-model'
import { usePlugins } from './usePlugins'
import { useStyleHold } from './useStyleHold'
import styles from './PluginsPage.module.css'

/** Shows the question a plugin is asking (`ui.confirm`), one at a time, over
 * whatever screen is open. Mounted once by the shell. */
export function PluginQuestionSurface() {
  const { model, state } = usePlugins()
  if (!state.asking) return null
  // Keyed by question so focus and the style hold start over for the next one.
  return <QuestionDialog key={state.asking.id} question={state.asking} onAnswer={(yes) => model.answer(state.asking!.id, yes)} />
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
