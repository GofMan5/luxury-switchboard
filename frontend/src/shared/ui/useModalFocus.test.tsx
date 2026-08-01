// @vitest-environment jsdom

import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { useModalFocus } from './useModalFocus'

function Fixture({ dismiss }: { dismiss: () => void }) {
  const ref = useModalFocus<HTMLDivElement>(dismiss)
  return <><button>Outside</button><div ref={ref}><button>First</button><button>Last</button></div></>
}

describe('useModalFocus', () => {
  it('moves focus inside, traps Tab and handles Escape', async () => {
    const dismiss = vi.fn()
    render(<Fixture dismiss={dismiss} />)
    const first = screen.getByRole('button', { name: 'First' })
    const last = screen.getByRole('button', { name: 'Last' })
    await waitFor(() => expect(document.activeElement).toBe(first))
    last.focus()
    fireEvent.keyDown(last, { key: 'Tab' })
    expect(document.activeElement).toBe(first)
    fireEvent.keyDown(first, { key: 'Escape' })
    expect(dismiss).toHaveBeenCalledOnce()
  })
})
