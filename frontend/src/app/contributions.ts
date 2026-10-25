import { useContext, useMemo, useSyncExternalStore } from 'react'
import { actionsFor, slotsFor, type ActionContribution, type ActionScope, type SlotContribution, type SlotScope } from '../shared/contracts/extensions'
import { ServicesContext } from './services'

const NOTHING = { slots: [], actions: [] } as const
const noSubscribe = () => () => {}
const noSnapshot = () => NOTHING

/** The slots and actions plugins contributed to one screen, kept current. A
 * screen rendered without the app's services (its own tests) sees none. */
export function useContributed(slotScope: SlotScope | undefined, actionScope?: ActionScope) {
  const extensions = useContext(ServicesContext)?.extensions
  const snapshot = useSyncExternalStore<{ readonly slots: readonly SlotContribution[]; readonly actions: readonly ActionContribution[] }>(extensions?.subscribe ?? noSubscribe, extensions?.snapshot ?? noSnapshot)
  const all = snapshot.slots
  const allActions = snapshot.actions
  const slots = useMemo(() => (slotScope ? slotsFor({ slots: all }, slotScope) : []), [all, slotScope])
  const actions = useMemo(() => (actionScope ? actionsFor({ actions: allActions }, actionScope) : []), [allActions, actionScope])
  return { slots, actions }
}

/** What an action's button announces: the title, and whose button it is. */
export function actionName(action: Pick<ActionContribution, 'title' | 'owner'>): string {
  return `${action.title} (from ${action.owner})`
}
