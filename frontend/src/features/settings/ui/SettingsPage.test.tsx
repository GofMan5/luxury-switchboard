// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Settings } from '../domain/settings'
import { SettingsForm } from './SettingsPage'

// Auto-cleanup needs vitest globals, which this project does not enable, so each
// render is torn down explicitly. Without it a later query matches two forms.
afterEach(cleanup)

const initial: Settings = {
  listenerPort: 8798,
  maxRequestMiB: 64,
  headerTimeoutSeconds: 45,
  streamIdleSeconds: 60,
  retryBaseMilliseconds: 500,
  retryMaxSeconds: 30,
  permanentAttempts: 2,
  maxQueued: 10_000,
  activityCapacity: 2_000,
  historyRetentionDays: 30,
  tunnelRetentionHours: 72,
  guardrailMode: 'monitor',
  guardrailFindings: 500,
  notificationsEnabled: true,
  providerHealthEnabled: true,
  animationsEnabled: true,
}

describe('SettingsForm', () => {
  it('carries the guardrail mode through a save it does not edit', async () => {
    // The control plane replaces the whole record, so a field this form never shows
    // must still be sent back exactly as it arrived.
    const onSave = vi.fn(async () => true)
    render(
      <SettingsForm
        initial={{ ...initial, guardrailMode: 'block' }}
        pending={false}
        restartRequired={false}
        error=""
        onSave={onSave}
        onRestart={vi.fn(async () => undefined)}
      />,
    )
    fireEvent.change(screen.getByLabelText(/Listener port/u), { target: { value: '8898' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save settings' }))
    expect(onSave).toHaveBeenCalledWith(expect.objectContaining({ listenerPort: 8898, guardrailMode: 'block' }))
  })

  it('keeps sequential numeric edits and enables save', () => {
    render(
      <SettingsForm
        initial={initial}
        pending={false}
        restartRequired={false}
        error=""
        onSave={vi.fn(async () => true)}
        onRestart={vi.fn(async () => undefined)}
      />,
    )
    fireEvent.change(screen.getByLabelText(/Listener port/u), { target: { value: '8898' } })
    fireEvent.change(screen.getByLabelText(/Maximum queued/u), { target: { value: '12000' } })
    expect((screen.getByLabelText(/Listener port/u) as HTMLInputElement).valueAsNumber).toBe(8898)
    expect((screen.getByLabelText(/Maximum queued/u) as HTMLInputElement).valueAsNumber).toBe(12000)
    expect((screen.getByRole('button', { name: 'Save settings' }) as HTMLButtonElement).disabled).toBe(false)
  })

  it('restarts from the saved-settings notice', () => {
    const onRestart = vi.fn(async () => undefined)
    render(
      <SettingsForm
        initial={initial}
        pending={false}
        restartRequired
        error=""
        onSave={vi.fn(async () => true)}
        onRestart={onRestart}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Restart now' }))
    expect(onRestart).toHaveBeenCalledOnce()
  })
})
