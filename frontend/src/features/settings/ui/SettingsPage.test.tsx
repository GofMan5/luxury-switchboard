// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ServicesContext, type AppServices } from '../../../app/services'
import type { Settings } from '../domain/settings'
import { BackupPanel, SettingsForm } from './SettingsPage'

// Auto-cleanup needs vitest globals, which this project does not enable, so each
// render is torn down explicitly. Without it a later query matches two forms.
afterEach(cleanup)

const initial: Settings = {
  listenerPort: 8798,
  maxRequestMiB: 64,
  headerTimeoutSeconds: 45,
  streamIdleSeconds: 60,
  heartbeatSeconds: 15,
  retryBaseMilliseconds: 500,
  retryMaxSeconds: 30,
  permanentAttempts: 2,
  maxQueued: 10_000,
  streamProbationMilliseconds: 250,
  activityCapacity: 2_000,
  historyRetentionDays: 30,
  tunnelRetentionHours: 72,
  guardrailMode: 'monitor', guardrailProviderModes: {},
  guardrailFindings: 500,
  notificationsEnabled: true,
  providerHealthEnabled: true,
  animationsEnabled: true,
  failoverEnabled: true,
  chainMode: 'balance',
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
        error=""
        onSave={onSave}
      />,
    )
    fireEvent.change(screen.getByLabelText(/^Listener port$/u), { target: { value: '8898' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save settings' }))
    expect(onSave).toHaveBeenCalledWith(expect.objectContaining({ listenerPort: 8898, guardrailMode: 'block' }))
  })

  it('keeps sequential numeric edits and enables save', () => {
    render(
      <SettingsForm
        initial={initial}
        pending={false}
        error=""
        onSave={vi.fn(async () => true)}
      />,
    )
    fireEvent.change(screen.getByLabelText(/^Listener port$/u), { target: { value: '8898' } })
    fireEvent.change(screen.getByLabelText(/^Maximum queued$/u), { target: { value: '12000' } })
    expect((screen.getByLabelText(/^Listener port$/u) as HTMLInputElement).valueAsNumber).toBe(8898)
    expect((screen.getByLabelText(/^Maximum queued$/u) as HTMLInputElement).valueAsNumber).toBe(12000)
    expect((screen.getByRole('button', { name: 'Save settings' }) as HTMLButtonElement).disabled).toBe(false)
  })

  it('applies on save and never asks for a restart', () => {
    render(<SettingsForm initial={initial} pending={false} error="" onSave={vi.fn(async () => true)} />)
    expect(screen.queryByRole('button', { name: /restart/i })).toBeNull()
    expect(screen.getByText(/applies the moment you save/i)).toBeTruthy()
  })

  it('resets one field to the saved value, and the whole tab to the shipped defaults', () => {
    render(
      <SettingsForm
        initial={{ ...initial, listenerPort: 8898, maxQueued: 12_000 }}
        pending={false}
        error=""
        onSave={vi.fn(async () => true)}
      />,
    )
    // The saved value differs from the shipped default: a preset click makes the
    // field dirty, its reset button must return the saved value, not the default.
    fireEvent.click(screen.getByRole('button', { name: '8,787' }))
    expect((screen.getByLabelText(/^Listener port$/u) as HTMLInputElement).valueAsNumber).toBe(8787)
    fireEvent.click(screen.getByRole('button', { name: /Reset Listener port/i }))
    expect((screen.getByLabelText(/^Listener port$/u) as HTMLInputElement).valueAsNumber).toBe(8898)
    // A field of another tab must keep its edit through this tab's reset.
    fireEvent.click(screen.getByRole('tab', { name: 'Reliability' }))
    fireEvent.change(screen.getByLabelText(/^Stream idle$/u), { target: { value: '300' } })
    fireEvent.click(screen.getByRole('tab', { name: 'Relay' }))
    // The tab reset aims at the defaults — every field of the tab at once.
    fireEvent.click(screen.getByRole('button', { name: /^defaults$/i }))
    expect((screen.getByLabelText(/^Listener port$/u) as HTMLInputElement).valueAsNumber).toBe(8798)
    expect((screen.getByLabelText(/^Maximum queued$/u) as HTMLInputElement).valueAsNumber).toBe(10_000)
    fireEvent.click(screen.getByRole('tab', { name: 'Reliability' }))
    expect((screen.getByLabelText(/^Stream idle$/u) as HTMLInputElement).valueAsNumber).toBe(300)
  })

  it('edits the new stream timers through typing, presets, and per-field reset', () => {
    render(
      <SettingsForm
        initial={{ ...initial, streamProbationMilliseconds: 350 }}
        pending={false}
        error=""
        onSave={vi.fn(async () => true)}
      />,
    )
    // Relay tab: the probation field types like any other number, and its preset
    // lands without typing. '500' is unique on this tab — maxQueued also ships a
    // 1,000 preset, so the click avoids that ambiguous name.
    fireEvent.change(screen.getByLabelText(/^Stream probation$/u), { target: { value: '750' } })
    expect((screen.getByLabelText(/^Stream probation$/u) as HTMLInputElement).valueAsNumber).toBe(750)
    fireEvent.click(screen.getByRole('button', { name: '500' }))
    expect((screen.getByLabelText(/^Stream probation$/u) as HTMLInputElement).valueAsNumber).toBe(500)
    // Reliability tab: every heartbeat preset text (15, 30, 60) also belongs to a
    // neighbour on this tab, so the click is scoped to the field's own row.
    fireEvent.click(screen.getByRole('tab', { name: 'Reliability' }))
    fireEvent.change(screen.getByLabelText(/^Stream heartbeat$/u), { target: { value: '45' } })
    expect((screen.getByLabelText(/^Stream heartbeat$/u) as HTMLInputElement).valueAsNumber).toBe(45)
    const heartbeatRow = screen.getByLabelText(/^Stream heartbeat$/u).closest('div') as HTMLElement
    fireEvent.click(within(heartbeatRow).getByRole('button', { name: '60' }))
    expect((screen.getByLabelText(/^Stream heartbeat$/u) as HTMLInputElement).valueAsNumber).toBe(60)
    expect((screen.getByRole('button', { name: 'Save settings' }) as HTMLButtonElement).disabled).toBe(false)
    // The presets made both fields dirty; the per-field reset returns the saved
    // value, not the shipped default — probation was saved as 350, not 250.
    fireEvent.click(within(heartbeatRow).getByRole('button', { name: /reset stream heartbeat/i }))
    expect((screen.getByLabelText(/^Stream heartbeat$/u) as HTMLInputElement).valueAsNumber).toBe(15)
    fireEvent.click(screen.getByRole('tab', { name: 'Relay' }))
    fireEvent.click(screen.getByRole('button', { name: /reset stream probation/i }))
    expect((screen.getByLabelText(/^Stream probation$/u) as HTMLInputElement).valueAsNumber).toBe(350)
  })
})

// A store with the backup model's public surface: the panel's contract is that
// it re-renders when the store notifies, so the fake notifies exactly like the
// real one and nothing else can carry the update into the tree.
interface FakeBackupSnapshot {
  readonly exporting: boolean
  readonly importing: boolean
  readonly lastExportPath: string
  readonly lastReport: object | null
  readonly error: string
}

class FakeBackupStore {
  #listeners = new Set<() => void>()
  #state: FakeBackupSnapshot = { exporting: false, importing: false, lastExportPath: '', lastReport: null, error: '' }
  #pending: (() => void) | undefined
  snapshot = (): FakeBackupSnapshot => this.#state
  subscribe = (listener: () => void): (() => void) => {
    this.#listeners.add(listener)
    return () => this.#listeners.delete(listener)
  }
  async export(): Promise<string> {
    this.#set({ ...this.#state, exporting: true })
    await new Promise<void>((resolve) => { this.#pending = resolve })
    this.#set({ ...this.#state, exporting: false, lastExportPath: 'C:/backups/switchboard.json' })
    return 'C:/backups/switchboard.json'
  }
  async import(): Promise<object | null> { return null }
  /** The test decides when the write settles, so the in-flight state is
   * asserted while it is genuinely in flight. */
  async settle(): Promise<void> {
    this.#pending?.()
    await new Promise((resolve) => setTimeout(resolve, 0))
  }
  #set(state: FakeBackupSnapshot): void {
    this.#state = state
    for (const listener of this.#listeners) listener()
  }
}

describe('BackupPanel', () => {
  it('shows the backup outcome as it happens, not on the next unrelated render', async () => {
    // The panel used to read one bare snapshot() per render with no
    // subscription: the model notified, nobody listened, and "Writing…" or the
    // written-path notice appeared only if some other state happened to
    // re-render the form. Nothing did, so the feedback never showed at all.
    const store = new FakeBackupStore()
    render(
      <ServicesContext.Provider value={{ backup: store } as unknown as AppServices}>
        <BackupPanel />
      </ServicesContext.Provider>,
    )
    fireEvent.click(screen.getByRole('button', { name: /export backup/i }))
    // In flight, from the store's own notification:
    expect((await screen.findByRole('button', { name: /writing…/i }) as HTMLButtonElement).disabled).toBe(true)
    await store.settle()
    // Settled, without any other state touching the form:
    expect(await screen.findByText(/backup written to/i)).toBeTruthy()
    expect(screen.getByText(/switchboard\.json/)).toBeTruthy()
    expect((screen.getByRole('button', { name: /export backup/i }) as HTMLButtonElement).disabled).toBe(false)
  })

  it('explains the format when the services context is absent', () => {
    render(<BackupPanel />)
    expect(screen.getByText('Plain text, no passphrase')).toBeTruthy()
    expect(screen.queryByRole('button', { name: /export backup/i })).toBeNull()
  })
})
