// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ServicesContext, type AppServices } from '../../../app/services'
import { ExtensionPoints } from '../../../shared/contracts/extensions'
import { PluginsModel } from '../application/plugins-model'
import { FakeControl, FakeWorkers, MemoryPluginsPort } from '../application/test-fakes'
import { EXAMPLE_PLUGINS } from '../domain/examples'
import type { PluginPackage } from '../domain/manifest'
import { checkTree } from '../domain/ui-tree'
import type { PageEvent } from '../domain/protocol'
import { InstallReviewDialog } from './PluginDialogs'
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
    const existing = {
      id: 'acme.tools', name: 'Acme tools', version: '1.0.0', author: 'Acme', description: '',
      permissions: ['ui:pages', 'control:sensitive'], granted: ['control:sensitive'], enabled: true,
      installedAt: '', updatedAt: '', sizeBytes: 1, hasScript: true, hasCss: false, hasTheme: false,
    }
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

  it('makes table rows keyboard-activatable and disables controls while the plugin is not running', () => {
    const onEvent = vi.fn()
    const raw = { type: 'stack', children: [
      { type: 'table', columns: [{ key: 'name', label: 'Name' }], rows: [{ id: 'n1', name: 'North' }], rowAction: 'open' },
      { type: 'button', text: 'Go', action: 'go' },
    ] }
    const { rerender } = render(<PluginTree disabled={false} onEvent={onEvent} tree={tree(raw)} />)
    const row = screen.getByText('North').closest('tr') as HTMLElement
    expect(row.tabIndex).toBe(0)
    fireEvent.keyDown(row, { key: 'Enter' })
    expect(onEvent).toHaveBeenCalledWith({ kind: 'action', action: 'open', row: 'n1', form: {} })

    rerender(<PluginTree disabled onEvent={onEvent} tree={tree(raw)} />)
    expect((screen.getByRole('button', { name: 'Go' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByText('North').closest('tr') as HTMLElement).hasAttribute('tabindex')).toBe(false)
  })

  it('renders text as text, never markup', () => {
    render(<PluginTree disabled={false} onEvent={() => undefined} tree={tree({ type: 'text', text: '<img src=x onerror=alert(1)>' })} />)
    expect(screen.getByText('<img src=x onerror=alert(1)>')).toBeTruthy()
    expect(document.querySelector('img')).toBeNull()
  })
})

async function page(safeMode = false) {
  const port = new MemoryPluginsPort()
  const workers = new FakeWorkers()
  const model = new PluginsModel({
    port, control: new FakeControl(), workers, extensions: new ExtensionPoints(), safeMode,
    notify: () => undefined, navigate: () => undefined, isRoute: () => true,
    app: { version: '1.0.0', capabilities: ['plugins.manage'] },
  })
  await model.connect()
  render(<ServicesContext value={{ plugins: model } as unknown as AppServices}><PluginsPage /></ServicesContext>)
  return { port, workers, model }
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
})
