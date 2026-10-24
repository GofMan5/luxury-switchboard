import { useMemo } from 'react'
import type { CommandContribution } from '../shared/contracts/extensions'
import { restartInSafeMode, safeMode } from '../shared/runtime/safe-mode'
import type { AppRoute, NavigationSection } from './navigation'

interface ShellCommandSources {
  readonly sections: readonly NavigationSection[]
  readonly pages: readonly { readonly id: string; readonly title: string }[]
  readonly navigate: (route: AppRoute) => void
  readonly relay: { readonly live: boolean; readonly pending: boolean; readonly toggle: () => Promise<void> }
  readonly providers: readonly { readonly id: string; readonly name: string; readonly enabled: boolean }[]
  readonly activeProviderId: string
  readonly activate: (id: string) => Promise<void>
  readonly toggleSidebar: () => void
  readonly contributed: readonly CommandContribution[]
}

/** The palette's catalog: the shell's own commands first (navigation, relay,
 * routing, window), then whatever slices and plugins contributed. */
export function useShellCommands(sources: ShellCommandSources): CommandContribution[] {
  const { sections, pages, navigate, relay, providers, activeProviderId, activate, toggleSidebar, contributed } = sources
  return useMemo(() => {
    const commands: CommandContribution[] = []
    for (const section of sections) {
      for (const item of section.items) {
        commands.push({
          id: `shell:go:${item.id}`,
          title: `Go to ${item.label}`,
          group: 'Navigate',
          keywords: [item.label, section.label],
          shortcut: item.key ? `Alt+${item.key}` : undefined,
          run: () => navigate(item.id),
        })
      }
    }
    for (const page of pages) {
      commands.push({ id: `shell:go:plugin/${page.id}`, title: `Go to ${page.title}`, group: 'Navigate', keywords: ['plugin', page.id], run: () => navigate(`plugin/${page.id}`) })
    }
    if (!relay.pending) {
      commands.push({
        id: 'shell:relay:toggle',
        title: relay.live ? 'Stop relay' : 'Start relay',
        group: 'Relay',
        keywords: ['listener', 'proxy', relay.live ? 'off' : 'on'],
        run: relay.toggle,
      })
    }
    for (const provider of providers) {
      if (!provider.enabled || provider.id === activeProviderId) continue
      commands.push({ id: `shell:route:${provider.id}`, title: `Route traffic to ${provider.name}`, group: 'Relay', keywords: ['provider', 'activate', 'switch'], run: () => activate(provider.id) })
    }
    commands.push(
      { id: 'shell:sidebar', title: 'Toggle sidebar', group: 'Window', keywords: ['collapse', 'expand', 'rail'], shortcut: 'Ctrl+B', run: toggleSidebar },
      {
        id: 'shell:safe-mode',
        title: safeMode ? 'Leave safe mode' : 'Restart in safe mode',
        group: 'Window',
        keywords: ['plugins', 'custom css', 'recover', 'reset'],
        shortcut: 'Ctrl+Alt+Shift+S',
        run: () => restartInSafeMode(!safeMode),
      },
      { id: 'shell:reload', title: 'Reload window', group: 'Window', keywords: ['refresh', 'restart'], run: () => window.location.reload() },
    )
    return [...commands, ...contributed]
  }, [sections, pages, navigate, relay.live, relay.pending, relay.toggle, providers, activeProviderId, activate, toggleSidebar, contributed])
}
