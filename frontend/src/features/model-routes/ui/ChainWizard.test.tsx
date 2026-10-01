// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ChainWizard } from './ModelRoutesPage'

// Auto-cleanup needs vitest globals, which this project does not enable, so each
// render is torn down explicitly; otherwise a later query matches two dialogs.
afterEach(cleanup)

const providers = [
  { id: 'alpha', name: 'Alpha' },
  { id: 'beta', name: 'Beta' },
]

function deferredCatalog() {
  let resolve!: (models: readonly string[]) => void
  const promise = new Promise<readonly string[]>((settle) => { resolve = settle })
  return { promise, resolve }
}

function renderWizard(discover: (providerId: string) => Promise<readonly string[]>) {
  const onSave = vi.fn(async () => undefined)
  render(
    <ChainWizard
      providers={providers}
      initialPublicModel=""
      pending={false}
      operationError=""
      onClose={() => undefined}
      discover={discover}
      onSave={onSave}
    />,
  )
  return { onSave }
}

describe('ChainWizard', () => {
  // The seed fires one discover per row back-to-back. With a single loading
  // slot the first answer cleared the second row's indicator, so a row still
  // waiting looked like a catalog that had come back empty.
  it('shows each row its own loading state until that row\'s provider answers', async () => {
    const alpha = deferredCatalog()
    const beta = deferredCatalog()
    renderWizard((providerId) => (providerId === 'alpha' ? alpha.promise : beta.promise))

    const waiting = await screen.findAllByLabelText('Upstream model')
    expect(waiting).toHaveLength(2)
    expect((waiting[0] as HTMLInputElement).placeholder).toBe('Loading…')
    expect((waiting[1] as HTMLInputElement).placeholder).toBe('Loading…')

    alpha.resolve(['alpha-model'])
    await waitFor(() => expect(screen.getAllByLabelText('Upstream model')[0]).toBeInstanceOf(HTMLSelectElement))
    // The first answer must not speak for the row that is still waiting.
    expect((screen.getAllByLabelText('Upstream model')[1] as HTMLInputElement).placeholder).toBe('Loading…')

    beta.resolve([])
    await waitFor(() => expect((screen.getAllByLabelText('Upstream model')[1] as HTMLInputElement).placeholder).toBe('Model name on this provider'))
  })

  // Rows are reorderable, so the key has to be the row, not the position: an
  // index key let React hand the focused input and its IME state to whichever
  // entry landed on that position after the move.
  it('keeps the input the user is typing in when its row moves', async () => {
    renderWizard(async () => [])
    const inputs = await screen.findAllByLabelText('Upstream model')
    fireEvent.change(inputs[0], { target: { value: 'first-model' } })
    fireEvent.change(inputs[1], { target: { value: 'second-model' } })
    inputs[1].focus()
    expect(document.activeElement).toBe(inputs[1])

    fireEvent.click(screen.getAllByRole('button', { name: 'Move earlier in the chain' })[1])

    const reordered = screen.getAllByLabelText('Upstream model')
    // The same DOM node — holding 'second-model' and the caret — is now the
    // first row; the move rearranged rows instead of rewriting their inputs.
    expect(reordered[0]).toBe(inputs[1])
    expect((reordered[0] as HTMLInputElement).value).toBe('second-model')
    expect((reordered[1] as HTMLInputElement).value).toBe('first-model')
    expect(document.activeElement).toBe(reordered[0])
  })
})
