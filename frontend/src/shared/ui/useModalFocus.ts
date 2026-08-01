import { useEffect, useRef } from 'react'

const focusableSelector = 'button, input, select, textarea, [tabindex]'

export function useModalFocus<T extends HTMLElement>(onDismiss: () => void, blocked = false) {
  const root = useRef<T>(null)
  const dismiss = useRef(onDismiss)
  const locked = useRef(blocked)
  dismiss.current = onDismiss
  locked.current = blocked

  useEffect(() => {
    const element = root.current
    if (!element) return
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null
    const focusable = () => [...element.querySelectorAll<HTMLElement>(focusableSelector)]
      .filter((item) => item.tabIndex >= 0 && !item.hasAttribute('disabled'))
    queueMicrotask(() => {
      if (element.isConnected && !element.contains(document.activeElement)) focusable()[0]?.focus()
    })
    const keydown = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !locked.current) {
        event.preventDefault()
        dismiss.current()
        return
      }
      if (event.key !== 'Tab') return
      const items = focusable()
      if (items.length === 0) {
        event.preventDefault()
        return
      }
      const first = items[0]
      const last = items[items.length - 1]
      if (event.shiftKey && (document.activeElement === first || !element.contains(document.activeElement))) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && (document.activeElement === last || !element.contains(document.activeElement))) {
        event.preventDefault()
        first.focus()
      }
    }
    element.addEventListener('keydown', keydown)
    return () => {
      element.removeEventListener('keydown', keydown)
      if (previous?.isConnected) previous.focus()
    }
  }, [])

  return root
}
