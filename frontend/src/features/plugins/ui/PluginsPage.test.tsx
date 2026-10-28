// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ServicesContext, type AppServices } from '../../../app/services'
import { ExtensionPoints } from '../../../shared/contracts/extensions'
import { PluginsModel } from '../application/plugins-model'
import { FakeControl, FakeWorkers, MemoryPluginsPort, summary, hostExtras } from '../application/test-fakes'
import { EXAMPLE_PLUGINS } from '../domain/examples'
import type { JournalEntry } from '../domain/journal'
import type { PluginPackage } from '../domain/manifest'
import { checkTree } from '../domain/ui-tree'
import type { PageEvent } from '../domain/protocol'
import type { PluginSetting } from '../domain/settings'
import { InstallReviewDialog } from './PluginDialogs'
import { QuestionDialog } from './PluginQuestion'
import { PluginTree } from './PluginTree'
import PluginsPage from './PluginsPage'

afterEach(cleanup)
// The first render pays for jsdom and the page's module graph; on a loaded
// machine running the whole suite that alone can pass the 5 s default.
vi.setConfig({ testTimeout: 20_000 })

const pkg: PluginPackage = {
  schema: 1, id: 'acme.tools', name: 'Acme tools', version: '2.0.0', author: 'Acme',
  permissions: ['ui:pages', 'control:read', 'control:sensitive'], script: 'switchboard.log("hi")',
}

describe('InstallReviewDialog', () => {
  it('starts credential access unticked and installs exactly what is ticked', () => {
    const onInstall = vi.fn()
    render(<InstallReviewDialog pkg={pkg} existing={undefined} pending={false} error="" onCancel={() => undefined} onInstall={onInstall} />)
    expect(screen.getByRole('dialog', { name: 'Install Acme tools?' })).toBeTruthy()
    const sensitive = screen.getByRole('checkbox', { name: /Credentials and exposure/u }) as HTMLInputElement
    const read = screen.getByRole('checkbox', { name: /Read relay state/u }) as HTMLInputElement
    expect(sensitive.checked).toBe(false)
    expect(read.checked).toBe(true)

    fireEvent.click(read)
    expect(screen.getByText(/Calls that need an unticked permission fail inside the plugin/u)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Install' }))
    expect(onInstall).toHaveBeenCalledWith(['ui:pages'])
  })

  it('keeps earlier choices on an update and marks new permissions', () => {
    const onInstall = vi.fn()
    const existing = summary({ version: '1.0.0', permissions: ['ui:pages', 'control:sensitive'], granted: ['control:sensitive'] })
    render(<InstallReviewDialog pkg={pkg} existing={existing} pending={false} error="" onCancel={() => undefined} onInstall={onInstall} />)
    expect(screen.getByText('1.0.0 → 2.0.0. Its stored data is kept and it is turned on.')).toBeTruthy()
    expect((screen.getByRole('checkbox', { name: /Credentials and exposure/u }) as HTMLInputElement).checked).toBe(true)
    expect((screen.getByRole('checkbox', { name: /^Pages/u }) as HTMLInputElement).checked).toBe(false)
    expect(within(screen.getByRole('checkbox', { name: /Read relay state/u }).closest('div') as HTMLElement).getByText('New')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Update' }))
    expect(onInstall).toHaveBeenCalledWith(['control:read', 'control:sensitive'])
  })

  it('blocks a package that cannot be sent and shows errors', () => {
    const huge = { ...pkg, permissions: ['ui:pages'] as const, script: '\\'.repeat(128 * 1024) } as PluginPackage
    render(<InstallReviewDialog pkg={huge} existing={undefined} pending={false} error="The control plane refused this package" onCancel={() => undefined} onInstall={() => undefined} />)
    expect((screen.getByRole('button', { name: 'Install' }) as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getAllByRole('alert').map((alert) => alert.textContent)).toEqual([expect.stringContaining('256 KiB'), 'The control plane refused this package'])
  })
})

function tree(raw: unknown) {
  const checked = checkTree(raw)
  if (!checked.ok) throw new Error(checked.problem)
  return checked.tree
}

describe('PluginTree', () => {
  it('sends actions with every named field value', () => {
    const events: PageEvent[] = []
    render(<PluginTree disabled={false} onEvent={(event) => events.push(event)} tree={tree({
      type: 'stack', children: [
        { type: 'input', name: 'q', label: 'Search', value: 'north' },
        { type: 'toggle', name: 'live', label: 'Live', value: false },
        { type: 'segmented', name: 'period', label: 'Period', options: ['1h', '24h'], value: '1h' },
        { type: 'button', text: 'Refresh', action: 'refresh' },
      ],
    })} />)
    fireEvent.click(screen.getByRole('switch', { name: 'Live' }))
    fireEvent.click(screen.getByRole('radio', { name: '24h' }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Search' }), { target: { value: 'south' } })
    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    expect(events).toEqual([
      { kind: 'change', name: 'live', value: true, form: { q: 'north', live: true, period: '1h' } },
      { kind: 'change', name: 'period', value: '24h', form: { q: 'north', live: true, period: '24h' } },
      { kind: 'action', action: 'refresh', form: { q: 'south', live: true, period: '24h' } },
    ])
  })

  it('gives each actionable table row a real button and disables controls while the plugin is not running', () => {
    const onEvent = vi.fn()
    const raw = { type: 'stack', children: [
      { type: 'table', columns: [{ key: 'name', label: 'Name' }, { key: 'ms', label: 'ms' }], rows: [{ id: 'n1', name: 'North', ms: 12 }, { id: 2, name: '', ms: 3 }], rowAction: 'open' },
      { type: 'button', text: 'Go', action: 'go' },
    ] }
    const { rerender } = render(<PluginTree disabled={false} onEvent={onEvent} tree={tree(raw)} />)
    // The keyboard reaches a row through the button in its first cell; the row itself is not a tab stop.
    const open = screen.getByRole('button', { name: 'North' }) as HTMLButtonElement
    expect(open.closest('td')?.cellIndex).toBe(0)
    expect(open.closest('tr')?.hasAttribute('tabindex')).toBe(false)
    open.focus()
    expect(document.activeElement).toBe(open)
    // Enter and Space on a focused <button> are clicks; jsdom does not synthesise them, so click directly.
    fireEvent.click(open)
    expect(onEvent).toHaveBeenLastCalledWith({ kind: 'action', action: 'open', row: 'n1', form: {} })
    // A row whose first cell is empty is still named, and the mouse may click anywhere on it.
    expect(screen.getByRole('button', { name: 'Row 2' })).toBeTruthy()
    fireEvent.click(screen.getByText('3'))
    expect(onEvent).toHaveBeenLastCalledWith({ kind: 'action', action: 'open', row: 2, form: {} })
    expect(onEvent).toHaveBeenCalledTimes(2)

    rerender(<PluginTree disabled onEvent={onEvent} tree={tree(raw)} />)
    expect((screen.getByRole('button', { name: 'Go' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: 'North' }) as HTMLButtonElement).disabled).toBe(true)
    fireEvent.click(screen.getByText('12'))
    expect(onEvent).toHaveBeenCalledTimes(2)
  })

  it('renders text as text, never markup', () => {
    render(<PluginTree disabled={false} onEvent={() => undefined} tree={tree({ type: 'text', text: '<img src=x onerror=alert(1)>' })} />)
    expect(screen.getByText('<img src=x onerror=alert(1)>')).toBeTruthy()
    expect(document.querySelector('img')).toBeNull()
  })
})

async function page(safeMode = false, capabilities: readonly string[] = ['plugins.manage']) {
  return pageWith(new MemoryPluginsPort(), capabilities, safeMode)
}

async function pageWith(port: MemoryPluginsPort, capabilities: readonly string[], safeMode = false) {
  const workers = new FakeWorkers()
  const { bus: _bus, ...extras } = hostExtras()
  const extensions = new ExtensionPoints()
  const model = new PluginsModel({
    ...extras, port, control: new FakeControl(), workers, extensions, safeMode,
    notify: () => undefined, navigate: () => undefined, isRoute: () => true, clipboard: async () => undefined,
    app: { version: '1.0.0', capabilities: ['plugins.manage'] },
  })
  await model.connect()
  render(<ServicesContext value={{ plugins: model, extensions, appVersion: '1.0.66', capabilities } as unknown as AppServices}><PluginsPage /></ServicesContext>)
  return { port, workers, model, extensions, files: extras.files }
}

describe('PluginsPage', () => {
  it('installs an example through the review and lists it', async () => {
    const { port, model } = await page()
    expect(screen.getByText('No plugins yet')).toBeTruthy()
    const example = EXAMPLE_PLUGINS[0]
    const card = screen.getByText(example.package.name).closest('li') as HTMLElement
    fireEvent.click(within(card).getByRole('button', { name: 'Install' }))
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Install' }))
    await waitFor(() => expect(screen.getByText(`${example.package.name} ${example.package.version} installed.`)).toBeTruthy())
    expect(port.plugins.map((plugin) => plugin.id)).toEqual([example.package.id])
    expect(screen.getByRole('switch', { name: `${example.package.name} enabled` }).getAttribute('aria-checked')).toBe('true')
    expect(within(card).getByRole('button', { name: 'Reinstall' })).toBeTruthy()
    model.dispose()
  })

  it('turns a plugin off, shows its details, and removes it after confirmation', async () => {
    const { port, model } = await page()
    await act(() => model.install(pkg, ['ui:pages']))
    fireEvent.click(screen.getByRole('switch', { name: 'Acme tools enabled' }))
    await waitFor(() => expect(port.plugins[0].enabled).toBe(false))
    await waitFor(() => expect(screen.getByText('Off')).toBeTruthy())

    fireEvent.click(screen.getByRole('button', { name: 'Details' }))
    expect(screen.getByText('acme.tools', { selector: 'code' })).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Remove Acme tools' }))
    const dialog = screen.getByRole('alertdialog', { name: 'Remove Acme tools?' })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Remove' }))
    await waitFor(() => expect(screen.getByText('Acme tools removed.')).toBeTruthy())
    expect(port.plugins).toEqual([])
    model.dispose()
  })

  it('shows what a plugin stores and clears it after confirmation', async () => {
    const { port, model } = await page()
    await act(() => model.install(pkg, ['ui:pages']))
    port.values.set('acme.tools', { cache: 'abc' })
    port.collections.set('acme.tools', { notes: { count: 2, bytes: 40 } })

    fireEvent.click(screen.getByRole('button', { name: 'Details' }))
    await waitFor(() => expect(screen.getByText(/^1 value,/u)).toBeTruthy())
    expect(screen.getByText(/^2 documents in 1 collection,/u)).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Clear data…' }))
    const dialog = screen.getByRole('alertdialog', { name: 'Clear data for Acme tools?' })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Clear data' }))
    await waitFor(() => expect(screen.getByText('Stored data for Acme tools cleared.')).toBeTruthy())
    expect(port.values.get('acme.tools')).toEqual({})
    expect(port.collections.has('acme.tools')).toBe(false)
    // The open details read again and there is nothing left to clear.
    await waitFor(() => expect(screen.getByText(/^No values,/u)).toBeTruthy())
    expect((screen.getByRole('button', { name: 'Clear data…' }) as HTMLButtonElement).disabled).toBe(true)
    model.dispose()
  })

  it('filters the log by level, then copies and saves the lines shown', async () => {
    const { workers, model, files } = await page()
    const clipboard = vi.spyOn(model, 'copyLog')
    await act(() => model.install(pkg, ['ui:pages']))
    act(() => {
      workers.last.send({ type: 'ready' })
      workers.last.send({ type: 'log', level: 'debug', text: 'cache warm' })
      workers.last.send({ type: 'log', level: 'warn', text: 'slow answer' })
    })
    fireEvent.click(screen.getByRole('button', { name: 'Details' }))
    const log = await screen.findByRole('list', { name: 'Plugin log' })
    expect(within(log).getByText('cache warm')).toBeTruthy()

    fireEvent.change(screen.getByLabelText('Level'), { target: { value: 'warn' } })
    expect(within(log).queryByText('cache warm')).toBeNull()
    expect(within(log).getByText('slow answer')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Copy' }))
    await waitFor(() => expect(screen.getByText('Copied 1 line.')).toBeTruthy())
    expect(clipboard.mock.calls[0]?.[0].map((entry) => entry.text)).toEqual(['slow answer'])

    fireEvent.click(screen.getByRole('button', { name: 'Save…' }))
    await waitFor(() => expect(screen.getByText('Saved 1 line.')).toBeTruthy())
    expect(files.saved).toHaveLength(1)
    expect(files.saved[0]?.name).toBe('acme.tools-log.txt')
    expect(files.saved[0]?.text).toMatch(/WARN {2}slow answer\n$/u)
    expect(files.saved[0]?.text).not.toContain('cache warm')

    fireEvent.change(screen.getByLabelText('Level'), { target: { value: 'error' } })
    expect(screen.getByText('No lines at this level.')).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Copy' }) as HTMLButtonElement).disabled).toBe(true)
    model.dispose()
  })

  it('explains a package file it cannot install', async () => {
    const { model } = await page()
    const input = screen.getByLabelText('Plugin package file') as HTMLInputElement
    const file = new File(['{"schema":2}'], 'broken.json', { type: 'application/json' })
    fireEvent.change(input, { target: { files: [file] } })
    await waitFor(() => expect(screen.getByRole('alertdialog', { name: 'broken.json is not a plugin package' })).toBeTruthy())
    expect(screen.getByText('"schema" must be 1.')).toBeTruthy()
    model.dispose()
  })

  it('says when safe mode is on', async () => {
    const { model } = await page(true)
    expect(screen.getByRole('status').textContent).toContain('Safe mode is on')
    expect(screen.getByRole('button', { name: 'Leave safe mode' })).toBeTruthy()
    model.dispose()
  })

  it('stays open after an install and starts folded when plugins already exist', async () => {
    const { model } = await page()
    await model.install(pkg, [])
    await screen.findByText('Acme tools')
    const examples = screen.getByText('Examples').closest('details') as HTMLDetailsElement
    expect(examples.open).toBe(true)
    model.dispose()
    cleanup()

    const seeded = new MemoryPluginsPort()
    await seeded.install(pkg, [])
    const again = await pageWith(seeded, ['plugins.manage'])
    await screen.findByText('Acme tools')
    const name = EXAMPLE_PLUGINS[0].package.name
    expect(screen.queryByText(name)).toBeNull()
    fireEvent.click(screen.getByText('Examples'))
    expect(await screen.findByText(name)).toBeTruthy()
    again.model.dispose()
  })

  it('loads a plugin from a folder, marks it Dev and unlinks it', async () => {
    const { model, port } = await page()
    fireEvent.click(screen.getByRole('button', { name: 'Load from folder…' }))
    const field = screen.getByLabelText('Folder with plugin.json')
    expect((screen.getByRole('button', { name: 'Load' }) as HTMLButtonElement).disabled).toBe(true)
    port.failNext = Object.assign(new Error('x'), { code: 'plugin_dev_limit' })
    fireEvent.change(field, { target: { value: 'C:\\dev\\plugin' } })
    fireEvent.click(screen.getByRole('button', { name: 'Load' }))
    await waitFor(() => expect(screen.getByText('No more plugin folders can be linked; unlink one first')).toBeTruthy())
    expect(screen.getByLabelText('Folder with plugin.json')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Load' }))
    await waitFor(() => expect(screen.getByText('Local plugin 1.0.0 loaded from its folder.')).toBeTruthy())
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.getByText('Dev')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Details' }))
    fireEvent.click(screen.getByRole('button', { name: /Unlink/u }))
    await waitFor(() => expect(screen.getByText(/no longer loaded from its folder/u)).toBeTruthy())
    expect(screen.queryByText('Dev')).toBeNull()
    model.dispose()
  })

  it('hides install-from-link unless the control plane downloads', async () => {
    const { model } = await page()
    expect(screen.queryByRole('button', { name: 'Install from link…' })).toBeNull()
    model.dispose()
  })

  it('downloads a package from a link into the usual review', async () => {
    const { model, port } = await page(false, ['plugins.manage', 'plugins.download'])
    const link = 'https://github.com/acme/tools'
    port.downloads[link] = pkg
    port.downloads['https://example.com/bad.json'] = { schema: 1, id: 'acme.bad', extra: true }
    fireEvent.click(screen.getByRole('button', { name: 'Install from link…' }))
    const field = screen.getByLabelText('Link')
    expect((screen.getByRole('button', { name: 'Download' }) as HTMLButtonElement).disabled).toBe(true)

    fireEvent.change(field, { target: { value: 'https://example.com/none.json' } })
    fireEvent.click(screen.getByRole('button', { name: 'Download' }))
    await waitFor(() => expect(screen.getByText('Nothing was found at that link.')).toBeTruthy())

    fireEvent.change(screen.getByLabelText('Link'), { target: { value: 'https://example.com/bad.json' } })
    fireEvent.click(screen.getByRole('button', { name: 'Download' }))
    await waitFor(() => expect(screen.getByText('The link does not hold a valid plugin package')).toBeTruthy())
    expect(screen.getByText('Unknown field "extra".')).toBeTruthy()

    fireEvent.change(screen.getByLabelText('Link'), { target: { value: ` ${link} ` } })
    fireEvent.click(screen.getByRole('button', { name: 'Download' }))
    const review = await screen.findByRole('dialog', { name: 'Install Acme tools?' })
    expect(within(review).getByText(link)).toBeTruthy()
    expect(port.plugins).toEqual([])
    fireEvent.click(within(review).getByRole('button', { name: 'Install' }))
    await waitFor(() => expect(screen.getByText('Acme tools 2.0.0 installed.')).toBeTruthy())
    model.dispose()
  })

  it('updates a plugin from the link it came from, only after the review', async () => {
    const { model, port } = await page(false, ['plugins.manage', 'plugins.download'])
    const link = 'https://example.test/acme/tools.json'
    await model.install({ ...pkg, version: '1.0.0' }, ['ui:pages'], link)
    await model.install({ ...pkg, id: 'acme.file', name: 'Acme file' }, [])
    port.downloads[link] = pkg
    expect(within(screen.getByText('Acme file').closest('li') as HTMLElement).queryByRole('button', { name: 'Update from link' })).toBeNull()

    const row = screen.getByText('Acme tools').closest('li') as HTMLElement
    fireEvent.click(within(row).getByRole('button', { name: 'Details' }))
    expect(within(row).getByText('Installed from')).toBeTruthy()
    expect(within(row).getByText(link)).toBeTruthy()
    fireEvent.click(within(row).getByRole('button', { name: 'Update from link' }))
    const review = await screen.findByRole('dialog', { name: 'Update Acme tools?' })
    expect(port.plugins.find((plugin) => plugin.id === 'acme.tools')?.version).toBe('1.0.0')
    fireEvent.click(within(review).getByRole('button', { name: 'Update' }))
    await waitFor(() => expect(screen.getByText('Acme tools 2.0.0 updated.')).toBeTruthy())
    expect(port.plugins.find((plugin) => plugin.id === 'acme.tools')?.source).toBe(link)
    model.dispose()
  })

  it('refuses an update when the link now serves a different plugin', async () => {
    const { model, port } = await page(false, ['plugins.manage', 'plugins.download'])
    const link = 'https://example.test/acme/tools.json'
    await model.install({ ...pkg, version: '1.0.0' }, ['ui:pages'], link)
    port.downloads[link] = { ...pkg, id: 'acme.other', name: 'Acme other' }
    const row = (await screen.findByText('Acme tools')).closest('li') as HTMLElement
    fireEvent.click(within(row).getByRole('button', { name: 'Details' }))
    fireEvent.click(within(row).getByRole('button', { name: 'Update from link' }))
    const review = await screen.findByRole('dialog', { name: 'Install Acme other?' })
    expect(within(review).getByRole('alert').textContent).toContain('The link now serves a different plugin')
    expect((within(review).getByRole('button', { name: 'Install' }) as HTMLButtonElement).disabled).toBe(true)
    model.dispose()
  })

  it('reopens the link dialog with the error when an update download fails', async () => {
    const { model } = await page(false, ['plugins.manage', 'plugins.download'])
    const link = 'https://example.test/acme/gone.json'
    await model.install({ ...pkg, version: '1.0.0' }, ['ui:pages'], link)
    const row = (await screen.findByText('Acme tools')).closest('li') as HTMLElement
    fireEvent.click(within(row).getByRole('button', { name: 'Details' }))
    fireEvent.click(within(row).getByRole('button', { name: 'Update from link' }))
    await waitFor(() => expect(screen.getByText('Nothing was found at that link.')).toBeTruthy())
    expect((screen.getByLabelText('Link') as HTMLInputElement).value).toBe(link)
    model.dispose()
  })

  it('checks linked plugins for updates and offers only newer versions', async () => {
    const { model, port } = await page(false, ['plugins.manage', 'plugins.download'])
    const newer = 'https://example.test/acme/tools.json'
    const gone = 'https://example.test/acme/gone.json'
    await model.install({ ...pkg, version: '1.0.0' }, ['ui:pages'], newer)
    await model.install({ ...pkg, id: 'acme.gone', name: 'Acme gone', version: '1.0.0' }, [], gone)
    port.downloads[newer] = pkg
    const check = await screen.findByRole('button', { name: 'Check for updates' })
    const before = port.plugins.map((plugin) => `${plugin.id}@${plugin.version}`)

    fireEvent.click(check)
    await waitFor(() => expect(screen.getByText('1 update available. 1 could not be checked.')).toBeTruthy())
    expect(port.plugins.map((plugin) => `${plugin.id}@${plugin.version}`)).toEqual(before)
    const row = screen.getByText('Acme tools').closest('li') as HTMLElement
    expect(within(row).getByText('Update 2.0.0 available')).toBeTruthy()
    const goneRow = screen.getByText('Acme gone').closest('li') as HTMLElement
    expect(within(goneRow).getByText(/The update check failed: Nothing was found at that link\./u)).toBeTruthy()

    delete port.downloads[newer]
    fireEvent.click(within(row).getByRole('button', { name: 'Update to 2.0.0' }))
    const review = await screen.findByRole('dialog', { name: 'Update Acme tools?' })
    fireEvent.click(within(review).getByRole('button', { name: 'Update' }))
    await waitFor(() => expect(screen.getByText('Acme tools 2.0.0 updated.')).toBeTruthy())
    expect(within(row).queryByText('Update 2.0.0 available')).toBeNull()
    model.dispose()
  })

  it('flags changed code under the same version and sends it to review', async () => {
    const { model, port } = await page(false, ['plugins.manage', 'plugins.download'])
    const link = 'https://example.test/acme/tools.json'
    await model.install(pkg, ['ui:pages'], link)
    port.downloads[link] = { ...pkg, script: `${pkg.script ?? ''}\n// patched` }

    fireEvent.click(await screen.findByRole('button', { name: 'Check for updates' }))
    await waitFor(() => expect(screen.getByText('No updates found. 1 link serves changed code under the installed version; review before reinstalling.')).toBeTruthy())
    expect(screen.queryByRole('button', { name: /Update all/u })).toBeNull()
    const row = screen.getByText('Acme tools').closest('li') as HTMLElement
    expect(within(row).getByText('Code changed without a new version')).toBeTruthy()

    fireEvent.click(within(row).getByRole('button', { name: 'Review changed code' }))
    const review = await screen.findByRole('dialog', { name: 'Update Acme tools?' })
    expect(within(review).getByText(/Reinstalls 2\.0\.0\./u)).toBeTruthy()
    fireEvent.click(within(review).getByRole('button', { name: 'Update' }))
    await waitFor(() => expect(within(row).queryByText('Code changed without a new version')).toBeNull())
    expect((port.packages.get('acme.tools') as PluginPackage).script).toMatch(/patched$/u)
    model.dispose()
  })

  it('updates every quiet plugin at once and leaves the rest for review', async () => {
    const { model, port } = await page(false, ['plugins.manage', 'plugins.download'])
    const quiet = 'https://example.test/acme/tools.json'
    const more = 'https://example.test/acme/more.json'
    await model.install({ ...pkg, version: '1.0.0' }, ['ui:pages'], quiet)
    await model.install({ ...pkg, id: 'acme.more', name: 'Acme more', version: '1.0.0', permissions: ['ui:pages'] }, ['ui:pages'], more)
    port.downloads[quiet] = pkg
    port.downloads[more] = { ...pkg, id: 'acme.more', name: 'Acme more' }
    expect(screen.queryByRole('button', { name: /Update all/u })).toBeNull()

    fireEvent.click(await screen.findByRole('button', { name: 'Check for updates' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Update all (1)' }))
    await waitFor(() => expect(screen.getByText(/^Updated Acme tools to 2\.0\.0\. 1 plugin needs your review/u)).toBeTruthy())
    expect(port.plugins.find((plugin) => plugin.id === 'acme.tools')).toMatchObject({ version: '2.0.0', granted: ['ui:pages'] })
    expect(port.plugins.find((plugin) => plugin.id === 'acme.more')?.version).toBe('1.0.0')
    expect(screen.queryByRole('button', { name: /Update all/u })).toBeNull()
    model.dispose()
  })

  it('shows no update check without a linked plugin', async () => {
    const { model } = await page(false, ['plugins.manage', 'plugins.download'])
    await model.install(pkg, [])
    await screen.findByText('Acme tools')
    expect(screen.queryByRole('button', { name: 'Check for updates' })).toBeNull()
    model.dispose()
  })

  it('offers no update from a link without download support or for a folder plugin', async () => {
    const { model } = await page()
    await model.install(pkg, [], 'https://example.test/acme/tools.json')
    await waitFor(() => expect(screen.getByText('Acme tools')).toBeTruthy())
    expect(screen.queryByRole('button', { name: 'Update from link' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Check for updates' })).toBeNull()
    model.dispose()
  })

  it('saves an installed package in the plugin pack format', async () => {
    const { model, port, files } = await page(false, ['plugins.manage', 'plugins.export'])
    const example = EXAMPLE_PLUGINS.find((entry) => entry.file === 'dusk-theme.json')
    if (!example) throw new Error('missing example')
    await model.install(example.package, example.package.permissions)
    fireEvent.click(await screen.findByRole('button', { name: 'Details' }))
    const save = screen.getByRole('button', { name: `Save ${example.package.name} package` })
    fireEvent.click(save)
    await waitFor(() => expect(screen.getByText(/saved as a package file/u)).toBeTruthy())
    expect(files.saved.at(-1)).toEqual({ name: 'example.dusk-theme-1.0.0.json', text: `${JSON.stringify(example.package, null, 2)}
` })

    port.failNext = Object.assign(new Error('gone'), { code: 'plugin_not_found' })
    fireEvent.click(save)
    expect(await screen.findByRole('alert')).toHaveProperty('textContent', expect.stringContaining('That plugin is no longer installed'))
    model.dispose()
  })

  it('hides save package when the control plane cannot export', async () => {
    const { model } = await page()
    await model.install(pkg, [])
    await waitFor(() => expect(screen.getByText('Acme tools')).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: 'Details' }))
    expect(screen.queryByRole('button', { name: /Save .* package/u })).toBeNull()
    model.dispose()
  })

  it('says which plugin arranged the rail and resets it', async () => {
    const { model, extensions } = await page()
    const reset = vi.fn(() => extensions.clear('plugin:acme.tools'))
    act(() => extensions.contribute('plugin:acme.tools', { layout: { owner: 'Acme tools', hidden: ['keys'], order: [], reset } }))
    expect(screen.getByText(/Layout customised by Acme tools/u)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Reset layout' }))
    expect(reset).toHaveBeenCalledOnce()
    expect(screen.queryByText(/Layout customised by/u)).toBeNull()
    model.dispose()
  })
})

function entry(seq: number, kind: JournalEntry['kind'], pluginId = 'acme.tools', extra: Partial<JournalEntry> = {}): JournalEntry {
  return { seq, at: `2026-10-${String(seq % 28 + 1).padStart(2, '0')}T10:00:00.000Z`, kind, pluginId, name: pluginId === 'acme.tools' ? 'Acme tools' : 'Gone plugin', version: '2.0.0', fromVersion: '', granted: [], revoked: [], origin: '', ...extra }
}

describe('plugin activity', () => {
  const journal = ['plugins.manage', 'plugins.journal']

  it('is hidden unless the control plane keeps a journal', async () => {
    const { port, model } = await page()
    expect(screen.queryByRole('heading', { name: 'Activity' })).toBeNull()
    expect(port.journalQueries).toEqual([])
    model.dispose()
  })

  it('lists entries newest first, filters by plugin, including removed ones, and reads older pages', async () => {
    const port = new MemoryPluginsPort()
    port.journal = [
      entry(70, 'removed', 'acme.gone'),
      entry(69, 'permissions', 'acme.tools', { granted: ['storage'], revoked: ['ui:pages'] }),
      entry(68, 'updated', 'acme.tools', { fromVersion: '1.0.0', origin: 'file' }),
      ...Array.from({ length: 60 }, (_, index) => entry(67 - index, 'enabled')),
    ]
    const { model } = await pageWith(port, journal)
    const list = await screen.findByRole('list', { name: 'Plugin activity' })
    expect(within(list).getAllByRole('listitem')).toHaveLength(50)
    expect(within(list).getAllByRole('listitem')[0].textContent).toContain('Removed Gone plugin')
    expect(screen.getByText('1.0.0 → 2.0.0')).toBeTruthy()
    expect(screen.getByText('from a file')).toBeTruthy()
    expect(screen.getByTitle('Granted storage').textContent).toMatch(/^\+ /u)
    expect(screen.getByTitle('Revoked ui:pages').textContent).toMatch(/^− /u)

    fireEvent.click(screen.getByRole('button', { name: 'Show older' }))
    await waitFor(() => expect(within(list).getAllByRole('listitem')).toHaveLength(63))
    expect(port.journalQueries.at(-1)).toEqual({ before: 21 })
    expect(screen.queryByRole('button', { name: 'Show older' })).toBeNull()

    fireEvent.change(screen.getByRole('combobox', { name: 'Plugin' }), { target: { value: 'acme.gone' } })
    await waitFor(() => expect(screen.getAllByRole('listitem').filter((item) => item.closest('ol[aria-label="Plugin activity"]'))).toHaveLength(1))
    expect(port.journalQueries.at(-1)).toEqual({ pluginId: 'acme.gone' })
    model.dispose()
  })

  it('drops an older page that answers after the list was read again', async () => {
    const port = new MemoryPluginsPort()
    port.journal = [entry(70, 'removed', 'acme.gone'), ...Array.from({ length: 60 }, (_, index) => entry(69 - index, 'enabled'))]
    const { model } = await pageWith(port, journal)
    await screen.findByRole('list', { name: 'Plugin activity' })
    let release = (): void => undefined
    const gate = new Promise<void>((resolve) => { release = resolve })
    const original = port.listJournal.bind(port)
    vi.spyOn(port, 'listJournal').mockImplementationOnce(async (query) => { await gate; return original(query) })
    fireEvent.click(screen.getByRole('button', { name: 'Show older' }))
    fireEvent.change(screen.getByRole('combobox', { name: 'Plugin' }), { target: { value: 'acme.gone' } })
    const rows = () => screen.getAllByRole('listitem').filter((item) => item.closest('ol[aria-label="Plugin activity"]'))
    await waitFor(() => expect(rows()).toHaveLength(1))
    release()
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(rows()).toHaveLength(1)
    model.dispose()
  })

  it('reports a failed older page without losing what is shown', async () => {
    const port = new MemoryPluginsPort()
    port.journal = Array.from({ length: 55 }, (_, index) => entry(55 - index, 'enabled'))
    const { model } = await pageWith(port, journal)
    await screen.findByRole('list', { name: 'Plugin activity' })
    port.failNext = Object.assign(new Error('locked'), { code: 'secure_storage_unavailable' })
    fireEvent.click(screen.getByRole('button', { name: 'Show older' }))
    expect((await screen.findByRole('alert')).textContent).toContain('Secure storage is unavailable')
    expect(screen.getAllByRole('listitem').filter((item) => item.closest('ol[aria-label="Plugin activity"]'))).toHaveLength(50)
    model.dispose()
  })

  it('clears every entry only after confirmation and records again afterwards', async () => {
    const port = new MemoryPluginsPort()
    port.journal = [entry(2, 'disabled'), entry(1, 'installed', 'acme.tools', { origin: 'link', granted: ['ui:pages'] })]
    const { model } = await pageWith(port, journal)
    await screen.findByRole('list', { name: 'Plugin activity' })
    fireEvent.click(screen.getByRole('button', { name: 'Clear…' }))
    let dialog = screen.getByRole('alertdialog', { name: 'Clear plugin activity?' })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(port.journal).toHaveLength(2)

    fireEvent.click(screen.getByRole('button', { name: 'Clear…' }))
    dialog = screen.getByRole('alertdialog', { name: 'Clear plugin activity?' })
    port.failNext = Object.assign(new Error('disk'), { code: 'plugin_journal_clear_failed' })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Clear' }))
    expect(await within(dialog).findByRole('alert')).toBeTruthy()
    expect(port.journal).toHaveLength(2)

    fireEvent.click(within(dialog).getByRole('button', { name: 'Clear' }))
    await waitFor(() => expect(screen.getByText('Nothing recorded yet')).toBeTruthy())
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(port.journal).toEqual([])
    expect((screen.getByRole('button', { name: 'Clear…' }) as HTMLButtonElement).disabled).toBe(true)
    model.dispose()
  })

  it('shows an unreadable journal as unavailable, not empty, and still offers clear', async () => {
    const port = new MemoryPluginsPort()
    port.failNext = Object.assign(new Error('locked'), { code: 'secure_storage_unavailable' })
    vi.spyOn(port, 'list').mockImplementationOnce(async () => [])
    const { model } = await pageWith(port, journal)
    expect((await screen.findByText(/^Unavailable:/u)).textContent).toContain('Secure storage is unavailable')
    expect(screen.queryByText('Nothing recorded yet')).toBeNull()
    expect((screen.getByRole('button', { name: 'Clear…' }) as HTMLButtonElement).disabled).toBe(false)
    model.dispose()
  })

  it('warns when the newest entries could not be saved', async () => {
    const port = new MemoryPluginsPort()
    port.journal = [entry(1, 'installed')]
    vi.spyOn(port, 'listJournal').mockResolvedValue({ entries: port.journal, next: 0, incomplete: true })
    const { model } = await pageWith(port, journal)
    expect(await screen.findByText(/could not be saved and will be lost/u)).toBeTruthy()
    model.dispose()
  })
})

const SETTINGS: readonly PluginSetting[] = [
  { key: 'limit', label: 'Row limit', description: 'Rows on the board.', type: 'number', default: 5, placeholder: '', min: 1, max: 50 },
  { key: 'compact', label: 'Compact rows', description: '', type: 'toggle', default: false },
  { key: 'period', label: 'Period', description: '', type: 'select', default: '1h', options: [{ value: '1h', label: 'Last hour' }, { value: '24h', label: 'Last day' }] },
]

describe('plugin settings, problems and questions', () => {
  it('edits declared settings, refuses a bad value and saves the rest in one write', async () => {
    const { port, model } = await page()
    await act(() => model.install({ ...pkg, permissions: ['ui:pages'], settings: SETTINGS }, ['ui:pages']))
    fireEvent.click(await screen.findByRole('button', { name: 'Acme tools settings' }))
    const dialog = await screen.findByRole('dialog', { name: 'Acme tools settings' })
    const limit = await within(dialog).findByRole('textbox', { name: 'Row limit' }) as HTMLInputElement
    expect(limit.value).toBe('5')
    expect(within(dialog).getByText('Rows on the board. Default: 5.')).toBeTruthy()
    const save = within(dialog).getByRole('button', { name: 'Save' }) as HTMLButtonElement
    expect(save.disabled).toBe(true)

    fireEvent.change(limit, { target: { value: '500' } })
    fireEvent.click(within(dialog).getByRole('switch', { name: /Compact rows/u }))
    fireEvent.change(within(dialog).getByRole('combobox', { name: 'Period' }), { target: { value: '24h' } })
    fireEvent.click(save)
    expect(await within(dialog).findByText('Enter a number from 1 to 50.')).toBeTruthy()
    expect(limit.getAttribute('aria-invalid')).toBe('true')
    expect(port.writes).toEqual([])

    fireEvent.change(limit, { target: { value: '12' } })
    fireEvent.click(save)
    await waitFor(() => expect(screen.getByText('Settings for Acme tools saved.')).toBeTruthy())
    expect(port.writes).toEqual([{ 'setting:limit': '12', 'setting:compact': 'true', 'setting:period': '"24h"' }])
    model.dispose()
  })

  it('stores a value equal to the default as no value', async () => {
    const { port, model } = await page()
    await act(() => model.install({ ...pkg, permissions: ['ui:pages'], settings: SETTINGS }, ['ui:pages']))
    port.values.set('acme.tools', { 'setting:limit': '9' })
    fireEvent.click(await screen.findByRole('button', { name: 'Acme tools settings' }))
    const dialog = await screen.findByRole('dialog', { name: 'Acme tools settings' })
    expect((await within(dialog).findByRole('textbox', { name: /Row limit/u }) as HTMLInputElement).value).toBe('9')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Reset to defaults' }))
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(port.writes).toEqual([{ 'setting:limit': null, 'setting:compact': null, 'setting:period': null }]))
    model.dispose()
  })

  it('shows why a stored plugin cannot run and keeps it off', async () => {
    const { port, model } = await page()
    port.plugins = [summary({ enabled: false, problem: 'Needs Luxury Switchboard 9.0.0 or later; this is 1.0.0.' })]
    await act(() => model.refresh())
    const toggle = screen.getByRole('switch', { name: 'Acme tools enabled' }) as HTMLButtonElement
    expect(toggle.disabled).toBe(true)
    expect(toggle.getAttribute('aria-checked')).toBe('false')
    expect(screen.getByText('Unavailable')).toBeTruthy()
    expect(screen.getByText('Needs Luxury Switchboard 9.0.0 or later; this is 1.0.0.')).toBeTruthy()
    model.dispose()
  })

  it('refuses a package that needs a newer app', () => {
    render(<InstallReviewDialog pkg={{ ...pkg, minAppVersion: '9.0.0' }} existing={undefined} appVersion="1.0.65" pending={false} error="" onCancel={() => undefined} onInstall={() => undefined} />)
    expect((screen.getByRole('button', { name: 'Install' }) as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getByRole('alert').textContent).toContain('needs Luxury Switchboard 9.0.0 or later; this is 1.0.65')
  })

  it('names the asking plugin and answers with the chosen button', () => {
    const onAnswer = vi.fn()
    render(<QuestionDialog question={{ id: 1, pluginName: 'Acme tools', title: 'Ban 3 clients?', body: 'They lose access now.', confirmText: 'Ban', tone: 'danger' }} onAnswer={onAnswer} />)
    const dialog = screen.getByRole('alertdialog', { name: 'Ban 3 clients?' })
    expect(within(dialog).getByText('Acme tools')).toBeTruthy()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Ban' }))
    expect(onAnswer).toHaveBeenLastCalledWith(true)
    fireEvent.keyDown(dialog, { key: 'Escape' })
    expect(onAnswer).toHaveBeenLastCalledWith(false)
  })
})