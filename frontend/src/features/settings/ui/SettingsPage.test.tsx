// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ServicesContext, type AppServices } from '../../../app/services'
import type { Settings } from '../domain/settings'
import type { UpdateCheck } from '../../updates/domain/update'
import type { UpdatesState } from '../../updates/application/updates-model'
import { BackupPanel, SettingsForm, UpdateStatusPanel } from './SettingsPage'

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
  updateCheckInterval: '1m',
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

  it('edits the automatic checking interval like any other setting', () => {
    render(
      <SettingsForm
        initial={{ ...initial, updateCheckInterval: '30m' }}
        pending={false}
        error=""
        onSave={vi.fn(async () => true)}
      />,
    )
    fireEvent.click(screen.getByRole('tab', { name: 'Updates' }))
    const interval = screen.getByLabelText(/^Automatic checking$/u) as HTMLSelectElement
    expect(interval.value).toBe('30m')
    // The select is the same kind of field as the numbers: an edit marks the
    // form dirty and lights the save bar.
    fireEvent.change(interval, { target: { value: 'off' } })
    expect(interval.value).toBe('off')
    expect((screen.getByRole('button', { name: 'Save settings' }) as HTMLButtonElement).disabled).toBe(false)
    // The per-field reset returns the saved interval, not the shipped default,
    // and a clean form closes its save bar rather than disabling it.
    fireEvent.click(screen.getByRole('button', { name: /Reset Automatic checking/i }))
    expect(interval.value).toBe('30m')
    expect(screen.queryByRole('button', { name: 'Save settings' })).toBeNull()
    // And the saved value rides through a save the form does not re-read.
    const onSave = vi.fn(async () => true)
    cleanup()
    render(
      <SettingsForm
        initial={{ ...initial, updateCheckInterval: '30m' }}
        pending={false}
        error=""
        onSave={onSave}
      />,
    )
    fireEvent.click(screen.getByRole('tab', { name: 'Updates' }))
    fireEvent.change(screen.getByLabelText(/^Automatic checking$/u), { target: { value: '1h' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save settings' }))
    expect(onSave).toHaveBeenCalledWith(expect.objectContaining({ updateCheckInterval: '1h' }))
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

// A store with the updates model's public surface: like the backup fake, it
// carries state changes into the tree through notifications alone, and it
// settles a manual check only when the test says so — never rejecting, exactly
// like the real model, whose failures land in checkError instead.
type FakeUpdatesOutcome = { check?: UpdateCheck; error?: Error }

class FakeUpdatesStore {
  #listeners = new Set<() => void>()
  #state: UpdatesState = { check: null, checking: false, checkError: '', installPhase: 'idle', installPercent: 0, installerPath: '', installError: '' }
  #pending: ((outcome: FakeUpdatesOutcome) => void) | undefined
  snapshot = (): UpdatesState => this.#state
  subscribe = (listener: () => void): (() => void) => {
    this.#listeners.add(listener)
    return () => this.#listeners.delete(listener)
  }
  /** The standing verdict of an automatic pass, before anyone clicks. */
  seed(check: UpdateCheck | null): void { this.#set({ ...this.#state, check }) }
  async check(): Promise<void> {
    this.#set({ ...this.#state, checking: true, checkError: '' })
    const outcome = await new Promise<FakeUpdatesOutcome>((resolve) => { this.#pending = resolve })
    this.#set(outcome.error
      ? { ...this.#state, checking: false, checkError: outcome.error.message }
      : { ...this.#state, check: outcome.check ?? null, checking: false })
  }
  async answer(check: UpdateCheck): Promise<void> {
    this.#pending?.({ check })
    await new Promise((resolve) => setTimeout(resolve, 0))
  }
  async failWith(error: Error): Promise<void> {
    this.#pending?.({ error })
    await new Promise((resolve) => setTimeout(resolve, 0))
  }
  #set(state: UpdatesState): void {
    this.#state = state
    for (const listener of this.#listeners) listener()
  }
}

describe('UpdateStatusPanel', () => {
  it('asks the feed now, and shows the verdict the moment it lands', async () => {
    const store = new FakeUpdatesStore()
    render(
      <ServicesContext.Provider value={{ updates: store } as unknown as AppServices}>
        <UpdateStatusPanel />
      </ServicesContext.Provider>,
    )
    fireEvent.click(screen.getByRole('button', { name: /check now/i }))
    // In flight, from the store's own notification, not from a prop change:
    expect((await screen.findByRole('button', { name: /checking…/i }) as HTMLButtonElement).disabled).toBe(true)
    await store.answer({ current: '1.0.50', latest: '1.0.50', url: '', newer: false, reachable: true, checkedAt: new Date().toISOString() })
    expect(await screen.findByText(/you are running the latest release/i)).toBeTruthy()
    expect(screen.getByText(/1\.0\.50 is the newest version/i)).toBeTruthy()
    expect((screen.getByRole('button', { name: /check now/i }) as HTMLButtonElement).disabled).toBe(false)
  })

  it('names an available release, with the running one beside it', () => {
    const store = new FakeUpdatesStore()
    // The standing answer of an automatic pass: the panel has to show it
    // without anyone pressing anything.
    store.seed({ current: '1.0.50', latest: '1.0.99', url: 'https://example/releases/1.0.99', newer: true, reachable: true, checkedAt: new Date().toISOString() })
    render(
      <ServicesContext.Provider value={{ updates: store } as unknown as AppServices}>
        <UpdateStatusPanel />
      </ServicesContext.Provider>,
    )
    expect(screen.getByText(/version 1\.0\.99 is available/i)).toBeTruthy()
    expect(screen.getByText(/you are running 1\.0\.50/i)).toBeTruthy()
  })

  it('says why a check it was asked for failed, out loud', async () => {
    const store = new FakeUpdatesStore()
    render(
      <ServicesContext.Provider value={{ updates: store } as unknown as AppServices}>
        <UpdateStatusPanel />
      </ServicesContext.Provider>,
    )
    fireEvent.click(screen.getByRole('button', { name: /check now/i }))
    await store.failWith(new Error('The release feed could not be reached'))
    expect((await screen.findByRole('alert')).textContent).toMatch(/could not be reached/u)
    expect((screen.getByRole('button', { name: /check now/i }) as HTMLButtonElement).disabled).toBe(false)
  })

  it('explains itself when the services context is absent', () => {
    render(<UpdateStatusPanel />)
    expect(screen.getByText(/checks happen in the backend/i)).toBeTruthy()
    expect(screen.queryByRole('button', { name: /check now/i })).toBeNull()
  })
})
