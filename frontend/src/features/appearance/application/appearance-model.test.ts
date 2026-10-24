import { describe, expect, it, vi } from 'vitest'
import { ControlPlaneError } from '../../../shared/contracts/protocol'
import { APPEARANCE_DEFAULTS, type Appearance } from '../domain/appearance'
import { AppearanceModel } from './appearance-model'
import type { AppearanceCache, AppearancePort } from './appearance-port'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail })
  return { promise, resolve, reject }
}

function memoryCache(initial: Appearance | null = null): AppearanceCache & { value: Appearance | null } {
  return { value: initial, read() { return this.value }, write(appearance) { this.value = appearance } }
}

function port(overrides: Partial<AppearancePort> = {}): AppearancePort & { emit: (appearance: Appearance) => void } {
  let listener: ((appearance: Appearance) => void) | null = null
  return {
    durable: true,
    get: vi.fn(async () => APPEARANCE_DEFAULTS),
    update: vi.fn(async (appearance: Appearance) => appearance),
    subscribe: (next) => { listener = next; return () => { listener = null } },
    emit: (appearance) => listener?.(appearance),
    ...overrides,
  }
}

describe('AppearanceModel', () => {
  it('starts from the cached record so the first frame is already the user\'s theme', () => {
    const model = new AppearanceModel(port(), memoryCache({ ...APPEARANCE_DEFAULTS, theme: 'moss' }))
    expect(model.snapshot()).toMatchObject({ phase: 'loading', appearance: { theme: 'moss' } })
  })

  it('applies a change at once, then persists it and caches the saved record', async () => {
    const store = port()
    const cache = memoryCache()
    const model = new AppearanceModel(store, cache)
    await model.connect()
    expect(model.update({ theme: 'carbon' })).toBe(true)
    expect(model.snapshot()).toMatchObject({ appearance: { theme: 'carbon' }, pending: true })
    await vi.waitFor(() => expect(model.snapshot().pending).toBe(false))
    expect(store.update).toHaveBeenCalledWith(expect.objectContaining({ theme: 'carbon' }))
    expect(cache.value?.theme).toBe('carbon')
  })

  it('coalesces rapid changes into one write of the latest record', async () => {
    const first = deferred<Appearance>()
    const update = vi.fn().mockReturnValueOnce(first.promise).mockImplementation(async (appearance: Appearance) => appearance)
    const model = new AppearanceModel(port({ update }), memoryCache())
    model.update({ accent: '#111111' })
    model.update({ accent: '#222222' })
    model.update({ accent: '#333333' })
    first.resolve({ ...APPEARANCE_DEFAULTS, accent: '#111111' })
    await vi.waitFor(() => expect(model.snapshot().pending).toBe(false))
    expect(update).toHaveBeenCalledTimes(2)
    expect(update.mock.calls[1]?.[0]).toMatchObject({ accent: '#333333' })
    expect(model.snapshot().appearance.accent).toBe('#333333')
  })

  it('refuses an invalid change without writing it', () => {
    const store = port()
    const model = new AppearanceModel(store, memoryCache())
    expect(model.update({ tokens: { '--accent': 'url(x)' } })).toBe(false)
    expect(model.snapshot().error).toMatch(/cannot carry/u)
    expect(store.update).not.toHaveBeenCalled()
  })

  it('puts the saved record back with the reason when the store refuses', async () => {
    const update = vi.fn().mockRejectedValue(new ControlPlaneError('storage_unavailable', 'encrypted storage is unavailable'))
    const model = new AppearanceModel(port({ update }), memoryCache())
    await model.connect()
    model.update({ theme: 'paper' })
    await vi.waitFor(() => expect(model.snapshot().pending).toBe(false))
    expect(model.snapshot()).toMatchObject({ appearance: { theme: 'graphite' }, error: 'Appearance was not saved: encrypted storage is unavailable.' })
  })

  it('follows changes made elsewhere, except while its own change is in flight', async () => {
    const write = deferred<Appearance>()
    const store = port({ update: vi.fn(() => write.promise) })
    const model = new AppearanceModel(store, memoryCache())
    await model.connect()
    store.emit({ ...APPEARANCE_DEFAULTS, theme: 'ink' })
    expect(model.snapshot().appearance.theme).toBe('ink')
    model.update({ theme: 'moss' })
    store.emit({ ...APPEARANCE_DEFAULTS, theme: 'carbon' })
    expect(model.snapshot().appearance.theme).toBe('moss')
    write.resolve({ ...APPEARANCE_DEFAULTS, theme: 'moss' })
    await vi.waitFor(() => expect(model.snapshot().pending).toBe(false))
  })

  it('keeps the cached record and says so when the store cannot be read', async () => {
    const model = new AppearanceModel(port({ get: vi.fn().mockRejectedValue(new Error('down')) }), memoryCache({ ...APPEARANCE_DEFAULTS, theme: 'ink' }))
    await model.connect()
    expect(model.snapshot()).toMatchObject({ phase: 'error', appearance: { theme: 'ink' } })
  })

  it('reset writes the defaults', async () => {
    const store = port()
    const model = new AppearanceModel(store, memoryCache({ ...APPEARANCE_DEFAULTS, theme: 'paper', customCss: 'h1{}' }))
    model.reset()
    await vi.waitFor(() => expect(model.snapshot().pending).toBe(false))
    expect(model.snapshot().appearance).toEqual(APPEARANCE_DEFAULTS)
  })
})
