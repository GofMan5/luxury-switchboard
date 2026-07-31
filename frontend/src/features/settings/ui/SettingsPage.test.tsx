// @vitest-environment jsdom

import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import type { Settings } from '../domain/settings'
import { SettingsForm } from './SettingsPage'

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
}

describe('SettingsForm', () => {
  it('keeps sequential numeric edits and enables save', () => {
    render(
      <SettingsForm
        initial={initial}
        pending={false}
        restartRequired={false}
        error=""
        onSave={vi.fn(async () => true)}
      />,
    )
    fireEvent.change(screen.getByLabelText(/Listener port/u), { target: { value: '8898' } })
    fireEvent.change(screen.getByLabelText(/Maximum queued/u), { target: { value: '12000' } })
    expect((screen.getByLabelText(/Listener port/u) as HTMLInputElement).valueAsNumber).toBe(8898)
    expect((screen.getByLabelText(/Maximum queued/u) as HTMLInputElement).valueAsNumber).toBe(12000)
    expect((screen.getByRole('button', { name: 'Save settings' }) as HTMLButtonElement).disabled).toBe(false)
  })
})
