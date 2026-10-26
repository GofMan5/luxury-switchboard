// Prompt snippets: reusable prompts, copied with one click from a page or the palette.
//
// What it shows:
// - a page (`ui:pages`) with a form (`input`, `textarea`) and a row of buttons per snippet; the
//   snippet id rides in the action name (`copy:<id>`), since a button carries no other payload;
// - `clipboard.writeText` (`ui:clipboard`): a page click or a palette command is the gesture it needs;
// - a document collection (`storage`) holding the snippets, listed in id order;
// - `files.save` / `files.open` (`files`) to export and import them, checking what comes back in;
// - one palette command per snippet (`ui:commands`), kept in step with the collection.
const MAX_SNIPPETS = 24
const snippets = switchboard.db.collection('snippets')
const STARTERS = [
  { title: 'Review this diff', text: 'Review the diff below for bugs, unclear names and missing tests. List the findings by severity.\n\n' },
  { title: 'Explain to a teammate', text: 'Explain what this code does to a teammate who is new to the project, in five sentences or fewer.\n\n' },
  { title: 'Commit message', text: 'Write a commit message for these changes: an imperative summary under 60 characters, then a short body.\n\n' },
]

await switchboard.ui.registerPage({ id: 'main', title: 'Snippets', icon: 'list' })

let notice = null // { tone, text } shown above the list after an action
let formKey = 0 // a new key remounts the form, which clears it after an add
let commands = new Set()

// Ids are `s<time><n>`, so the collection's id order is the order snippets were added.
const newId = (n) => `s${Date.now()}${String(n).padStart(2, '0')}`

async function load() {
  const all = []
  let after
  do {
    const page = await snippets.list({ limit: 200, after })
    all.push(...page.items.map((item) => ({ id: item.id, title: item.doc.title, text: item.doc.text })))
    after = page.next
  } while (after)
  return all
}

const valid = (snippet) =>
  typeof snippet?.title === 'string' && typeof snippet?.text === 'string' &&
  snippet.title.trim().length > 0 && snippet.title.length <= 60 &&
  snippet.text.trim().length > 0 && snippet.text.length <= 4000

async function syncCommands(list) {
  const wanted = new Set(list.map((snippet) => `copy.${snippet.id}`))
  for (const id of commands) if (!wanted.has(id)) await switchboard.commands.unregister(id)
  // Registering an id again replaces it, so a renamed snippet just updates its command.
  for (const snippet of list) {
    await switchboard.commands.register(
      { id: `copy.${snippet.id}`, title: `Copy snippet: ${snippet.title}`, group: 'Snippets', keywords: ['snippet', 'prompt'] },
      () => switchboard.clipboard.writeText(snippet.text),
    )
  }
  commands = wanted
}

function preview(text) {
  const line = text.trim().split('\n')[0]
  return line.length > 90 ? `${line.slice(0, 89)}…` : line
}

function snippetRow(snippet) {
  return {
    type: 'row', key: snippet.id, align: 'between', wrap: false, children: [
      { type: 'stack', gap: 'none', children: [
        { type: 'text', text: snippet.title, weight: 'strong' },
        { type: 'text', text: preview(snippet.text), tone: 'muted', size: 'sm' },
      ] },
      { type: 'row', gap: 'sm', wrap: false, children: [
        { type: 'button', text: 'Copy', action: `copy:${snippet.id}`, variant: 'primary' },
        { type: 'button', text: 'Remove', action: `remove:${snippet.id}`, variant: 'ghost' },
      ] },
    ],
  }
}

async function render(list) {
  const full = list.length >= MAX_SNIPPETS
  await switchboard.ui.render('main', {
    type: 'stack',
    children: [
      notice && { type: 'callout', tone: notice.tone, text: notice.text },
      {
        type: 'panel',
        title: 'Snippets',
        subtitle: `${list.length} of ${MAX_SNIPPETS}. Copy one here, or from the palette (Ctrl+K).`,
        actions: [
          { type: 'button', text: 'Import…', action: 'import', variant: 'ghost', disabled: full },
          { type: 'button', text: 'Export…', action: 'export', variant: 'ghost', disabled: list.length === 0 },
        ],
        children: list.length === 0
          ? [{ type: 'empty', title: 'No snippets yet', hint: 'Add one below, or import a file.' }]
          : list.map(snippetRow),
      },
      {
        type: 'panel', key: `form-${formKey}`, title: 'New snippet', children: [
          { type: 'input', name: 'title', label: 'Title', maxLength: 60, required: true, placeholder: 'Summarise a thread' },
          { type: 'textarea', name: 'text', label: 'Text', hint: 'Ctrl+Enter adds it.', maxLength: 4000, rows: 5, required: true, action: 'add' },
          { type: 'button', text: 'Add snippet', action: 'add', variant: 'primary', disabled: full },
        ],
      },
    ],
  })
}

async function add(form, list) {
  const snippet = { title: String(form.title ?? '').trim(), text: String(form.text ?? '') }
  if (!valid(snippet)) return { tone: 'warning', text: 'Give the snippet a title (up to 60 characters) and some text.' }
  if (list.length >= MAX_SNIPPETS) return { tone: 'warning', text: `Keep at most ${MAX_SNIPPETS} snippets; remove one first.` }
  await snippets.put(newId(0), snippet)
  formKey += 1
  return { tone: 'success', text: `Added "${snippet.title}".` }
}

async function exportAll(list) {
  const text = JSON.stringify({ snippets: list.map(({ title, text }) => ({ title, text })) }, null, 2)
  const saved = await switchboard.files.save({ name: 'snippets.json', text, mime: 'application/json' })
  return saved ? { tone: 'success', text: `Exported ${list.length} snippets.` } : null
}

async function importFile(list) {
  const file = await switchboard.files.open({ accept: ['.json'], maxBytes: 256 * 1024 })
  if (!file) return null
  let parsed
  try {
    parsed = JSON.parse(file.text)
  } catch {
    return { tone: 'danger', text: `${file.name} is not JSON.` }
  }
  // A file is untrusted input: keep only well-formed snippets and skip titles already here.
  const incoming = (Array.isArray(parsed?.snippets) ? parsed.snippets : []).filter(valid)
  const titles = new Set(list.map((snippet) => snippet.title))
  let added = 0
  for (const snippet of incoming) {
    const title = snippet.title.trim()
    if (list.length + added >= MAX_SNIPPETS) break
    if (titles.has(title)) continue
    titles.add(title)
    await snippets.put(newId(added), { title, text: snippet.text })
    added += 1
  }
  return { tone: added > 0 ? 'success' : 'info', text: `Imported ${added} of ${incoming.length} snippets from ${file.name}.` }
}

switchboard.ui.onEvent('main', async (event) => {
  if (event.kind !== 'action') return
  const [verb, id] = event.action.split(':')
  const list = await load()
  const snippet = list.find((entry) => entry.id === id)
  try {
    if (verb === 'copy' && snippet) {
      await switchboard.clipboard.writeText(snippet.text)
      notice = { tone: 'success', text: `Copied "${snippet.title}".` }
    } else if (verb === 'remove' && snippet) {
      await snippets.delete(snippet.id)
      notice = { tone: 'info', text: `Removed "${snippet.title}".` }
    } else if (verb === 'add') notice = await add(event.form ?? {}, list)
    else if (verb === 'export') notice = await exportAll(list)
    else if (verb === 'import') notice = await importFile(list)
  } catch (error) {
    notice = { tone: 'danger', text: error.message }
  }
  const current = await load()
  await syncCommands(current)
  await render(current)
})

// First run: a few starters, so the page and the palette have something to show.
if ((await snippets.list({ limit: 1 })).items.length === 0) {
  for (const [n, snippet] of STARTERS.entries()) await snippets.put(newId(n), snippet)
}
const list = await load()
await syncCommands(list)
await render(list)
