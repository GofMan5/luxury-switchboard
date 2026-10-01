// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { useState } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { WorkspaceBoundary } from './WorkspaceBoundary'

// Auto-cleanup needs vitest globals, which this project does not enable.
afterEach(cleanup)

describe('WorkspaceBoundary', () => {
  // The fallback's only control had never been clicked by any test. "Try
  // again" must put the workspace back up: the child mounts again, and a
  // screen that fails the same way twice says so — the alert returns. The
  // throw rides a state update rather than the first render, the path the
  // shell test uses, so the initial mount itself stays clean.
  it('re-attempts the workspace when the user clicks Try again', () => {
    // React reports every caught error on the console, and so does the boundary.
    const reported = vi.spyOn(console, 'error').mockImplementation(() => {})
    let attempts = 0
    function Flaky() {
      attempts += 1
      // The shape of the bug the boundary exists for: a record that was fine
      // when the screen mounted and lost the field a cell reads on update.
      const [broken, setBroken] = useState(false)
      if (broken) throw new Error('the record was missing count')
      return <button type="button" onClick={() => setBroken(true)}>Break the record</button>
    }

    render(<WorkspaceBoundary><Flaky /></WorkspaceBoundary>)
    expect(screen.getByRole('button', { name: 'Break the record' })).toBeTruthy()
    expect(screen.queryByRole('alert')).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Break the record' }))
    expect(screen.getByRole('alert')).toBeTruthy()
    const brokenAttempts = attempts

    // The click: the failed screen comes down, the child gets a second mount,
    // and the boundary stops claiming failure while that attempt is up.
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
    expect(attempts).toBeGreaterThan(brokenAttempts)
    expect(screen.getByRole('button', { name: 'Break the record' })).toBeTruthy()
    expect(screen.queryByRole('alert')).toBeNull()

    // The re-attempted screen can fail the same way, and the boundary holds it.
    fireEvent.click(screen.getByRole('button', { name: 'Break the record' }))
    expect(screen.getByRole('alert')).toBeTruthy()
    reported.mockRestore()
  })
})
