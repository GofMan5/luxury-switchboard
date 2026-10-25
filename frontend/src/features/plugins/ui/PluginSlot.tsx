import { useCallback, useEffect, useState } from 'react'
import type { SlotContext } from '../../../shared/contracts/extensions'
import type { PageEvent } from '../domain/protocol'
import { PluginTree, type FormValues } from './PluginTree'
import { usePlugins } from './usePlugins'
import styles from './PluginsPage.module.css'

/** What a plugin drew on one of its slot surfaces (`widget:`, `inspector:`,
 * `settings:`). The shell draws the frame and error boundary around it; the
 * plugin is told when the surface is shown — with what an inspector tab is
 * showing — and hidden. */
export function PluginSlot({ pluginId, surface, context }: { readonly pluginId: string; readonly surface: string; readonly context: SlotContext }) {
  const { model, state } = usePlugins()
  const slot = state.surfaces[`${pluginId}/${surface}`]
  const running = state.runtimes[pluginId]?.state === 'running'
  const registered = slot !== undefined
  const [values] = useState<FormValues>(() => new Map())
  const send = useCallback((event: PageEvent) => model.sendSurfaceEvent(pluginId, surface, event), [model, pluginId, surface])
  const { scope, id } = context

  useEffect(() => {
    if (!registered || !running) return
    const shown = { scope, ...(id === undefined ? {} : { id }) }
    send({ kind: 'show', context: shown, form: Object.fromEntries(values) })
    return () => send({ kind: 'hide', context: shown, form: Object.fromEntries(values) })
  }, [registered, running, send, values, scope, id])

  if (!slot) return null
  return (
    <>
      {slot.problem ? <p className={styles.notice} data-tone="danger" role="alert">The plugin sent something this window cannot draw: {slot.problem}</p> : null}
      {slot.tree ? <PluginTree tree={slot.tree} disabled={!running} onEvent={send} values={values} canvas={model.canvas(pluginId)} surface={surface} /> : <p className={styles.muted}>Loading…</p>}
    </>
  )
}
