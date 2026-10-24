import { useId, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { CornerDownLeft, Search } from 'lucide-react'
import type { CommandContribution } from '../shared/contracts/extensions'
import { useModalFocus } from '../shared/ui/useModalFocus'
import { scoreCommand } from './command-score'
import styles from './CommandPalette.module.css'

const RESULT_LIMIT = 60

export function CommandPalette({ commands, onClose }: { readonly commands: readonly CommandContribution[]; readonly onClose: () => void }) {
  const root = useModalFocus<HTMLDivElement>(onClose)
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)
  const [failure, setFailure] = useState('')
  const listId = useId()
  const listRef = useRef<HTMLUListElement>(null)

  const results = useMemo(() => {
    const scored = commands
      .map((command, order) => ({ command, order, score: scoreCommand(query, command) }))
      .filter((entry) => entry.score > 0)
    if (query.trim()) scored.sort((left, right) => right.score - left.score || left.order - right.order)
    return scored.slice(0, RESULT_LIMIT).map((entry) => entry.command)
  }, [commands, query])

  const current = Math.min(active, Math.max(results.length - 1, 0))

  const run = async (command: CommandContribution | undefined) => {
    if (!command) return
    onClose()
    try {
      await command.run()
    } catch {
      setFailure(`${command.title} did not complete`)
    }
  }

  const move = (delta: number) => {
    if (results.length === 0) return
    const next = (current + delta + results.length) % results.length
    setActive(next)
    listRef.current?.querySelector<HTMLElement>(`[data-index="${next}"]`)?.scrollIntoView({ block: 'nearest' })
  }

  const keydown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'ArrowDown') { event.preventDefault(); move(1) }
    else if (event.key === 'ArrowUp') { event.preventDefault(); move(-1) }
    else if (event.key === 'PageDown') { event.preventDefault(); move(8) }
    else if (event.key === 'PageUp') { event.preventDefault(); move(-8) }
    else if (event.key === 'Enter') { event.preventDefault(); void run(results[current]) }
  }

  let lastGroup = ''
  return (
    <div className={styles.scrim} onMouseDown={(event) => { if (event.target === event.currentTarget) onClose() }}>
      <div ref={root} className={styles.palette} role="dialog" aria-modal="true" aria-label="Command palette">
        <label className={styles.search}>
          <Search size={15} aria-hidden="true" />
          <span className="sr-only">Search commands</span>
          <input
            data-autofocus
            role="combobox"
            aria-expanded="true"
            aria-controls={listId}
            aria-activedescendant={results[current] ? `${listId}-${current}` : undefined}
            aria-autocomplete="list"
            autoComplete="off"
            spellCheck={false}
            placeholder="Jump to a page, run an action, switch a theme…"
            value={query}
            onChange={(event) => { setQuery(event.target.value); setActive(0); setFailure('') }}
            onKeyDown={keydown}
          />
          <kbd>Esc</kbd>
        </label>
        <ul ref={listRef} id={listId} className={styles.results} role="listbox" aria-label="Commands">
          {results.map((command, index) => {
            const heading = command.group !== lastGroup ? command.group : ''
            lastGroup = command.group
            return (
              <li key={command.id} role="presentation">
                {heading ? <span className={styles.group} aria-hidden="true">{heading}</span> : null}
                <div
                  id={`${listId}-${index}`}
                  role="option"
                  aria-selected={index === current}
                  data-index={index}
                  className={styles.option}
                  onMouseMove={() => { if (index !== current) setActive(index) }}
                  onClick={() => void run(command)}
                >
                  <span className={styles.title}>{command.title}</span>
                  {command.shortcut ? <kbd className={styles.shortcut}>{command.shortcut}</kbd> : null}
                  {index === current ? <CornerDownLeft className={styles.enter} size={13} aria-hidden="true" /> : null}
                </div>
              </li>
            )
          })}
          {results.length === 0 ? <li className={styles.none} role="presentation">No command matches “{query.trim()}”.</li> : null}
        </ul>
        <footer className={styles.footer}>
          <span><kbd>↑</kbd><kbd>↓</kbd> move</span>
          <span><kbd>Enter</kbd> run</span>
          <span className={styles.count}>{failure || `${results.length} of ${commands.length}`}</span>
        </footer>
      </div>
    </div>
  )
}
