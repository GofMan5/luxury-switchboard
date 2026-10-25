// Pinned requests: an Overview widget, an inspector action and a header button.
//
// What it shows:
// - `ui.registerAction` (scope `activity`): a button in the request inspector; the click arrives at
//   `ui.onEvent` as `{ kind: 'action', action, scope, target }` with `pageId: ''`, `target` being the
//   request id;
// - `ui.registerWidget`: an Overview widget drawn with `ui.render('widget:<id>', tree)`;
// - `ui.registerHeaderButton`: an icon in the top bar, here opening a `ui.dialog`;
// - `db.collection` (`storage`) for the pins, so they survive restarts.
//
// The window labels every slot "From Pinned requests", and they all go when the plugin stops.
const MAX_PINS = 20
const pins = switchboard.db.collection('pins')

const { surface } = await switchboard.ui.registerWidget({ id: 'pins', title: 'Pinned requests', order: 20 })
await switchboard.ui.registerAction({ id: 'pin', title: 'Pin request', scope: 'activity', icon: 'list' })
await switchboard.ui.registerHeaderButton({ id: 'review', title: 'Review pinned requests', icon: 'list' })

/** Newest first. Ids are `<time>-<request id>` so the collection's id order is time order. */
async function load() {
  const { items } = await pins.list({ limit: MAX_PINS })
  return items.map((item) => item.doc).reverse()
}

function table(rows) {
  return {
    type: 'table',
    columns: [{ key: 'request', label: 'Request', mono: true }, { key: 'pinned', label: 'Pinned' }],
    rows: rows.map((pin) => ({ request: pin.request, pinned: pin.at.slice(0, 16).replace('T', ' ') })),
    empty: 'Nothing pinned. Use "Pin request" in the Activity inspector.',
  }
}

async function renderWidget() {
  await switchboard.ui.render(surface, table((await load()).slice(0, 5)))
}

async function pin(request) {
  const at = new Date().toISOString()
  const all = await load()
  if (all.some((entry) => entry.request === request)) return
  await pins.put(`${Date.parse(at)}-${request}`, { request, at })
  // Keep the newest MAX_PINS: drop the oldest ids.
  const { items } = await pins.list({ limit: 200 })
  for (const item of items.slice(0, Math.max(0, items.length - MAX_PINS))) await pins.delete(item.id)
}

async function review() {
  // Header and action clicks count as a gesture, which `ui.dialog` needs.
  const dialog = await switchboard.ui.dialog({
    title: 'Pinned requests',
    tree: table(await load()),
    actions: [{ id: 'clear', label: 'Clear all', tone: 'danger' }, { id: 'close', label: 'Close', tone: 'primary' }],
  })
  const { action } = await dialog.result
  if (action === 'clear') await pins.clear()
}

switchboard.ui.onEvent(async (event) => {
  if (event.kind === 'show' && event.pageId === surface) return renderWidget()
  if (event.kind !== 'action' || event.pageId !== '') return
  if (event.action === 'pin' && event.target) await pin(event.target)
  if (event.action === 'review') await review()
  await renderWidget()
})

await renderWidget()
