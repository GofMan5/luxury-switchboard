// Rule pack: request and retry rules installed by a plugin.
//
// What it shows:
// - `switchboard.rules` (`relay:rules`): save, list and delete rules, and read recent errors;
// - ownership: the Switchboard records this plugin as the owner of what it saves, so it can change
//   only its own rules, the operator can still edit or disable them on the Rules page, and they are
//   deleted with the plugin;
// - a retry rule matches errors only: it runs after the content-policy check, so a refusal is never
//   retried, and the built-in retry patterns stay hidden (they run when no custom rule matches).
const PAGE = 'pack';

// The pack. Ids are this plugin's own; saving the same id again replaces the rule.
const REQUEST_RULES = [
  {
    id: 'pack-drafts',
    name: 'Tag draft models',
    enabled: true,
    match: { models: ['draft-*'], endpoints: ['chat', 'responses'] },
    effects: { tags: ['drafts'], rateLimit: { requests: 60, windowSeconds: 60 } },
  },
]
const RETRY_RULES = [
  {
    id: 'pack-overloaded',
    name: 'Overloaded: try the next provider',
    enabled: true,
    match: { statuses: [503, 529], contains: ['overloaded'], errorCodes: [], providers: [], models: [] },
    action: 'switch_provider',
    maxAttempts: 2,
    delayMs: 500,
  },
]
const OURS = new Set([...REQUEST_RULES, ...RETRY_RULES].map((rule) => rule.id))

let state = { installed: [], errors: [], problem: '' }

async function refresh() {
  try {
    const [{ requestRules, retryRules }, { errors }] = await Promise.all([switchboard.rules.list(), switchboard.rules.recentErrors(10)])
    state = {
      installed: [...requestRules, ...retryRules].filter((rule) => OURS.has(rule.id)),
      errors: errors.filter((error) => !error.coveredBy),
      problem: '',
    }
  } catch (error) {
    state = { ...state, problem: error.message }
  }
}

async function install() {
  // One rule at a time: a refused rule (`invalid_rule`, `limit`, `permission_denied`) names its problem.
  for (const rule of REQUEST_RULES) await switchboard.rules.saveRequestRule(rule)
  for (const rule of RETRY_RULES) await switchboard.rules.saveRetryRule(rule)
}

async function remove() {
  for (const rule of REQUEST_RULES) await switchboard.rules.deleteRequestRule(rule.id).catch(() => {})
  for (const rule of RETRY_RULES) await switchboard.rules.deleteRetryRule(rule.id).catch(() => {})
}

function view() {
  const installed = state.installed.length === OURS.size
  return {
    type: 'stack',
    gap: 'lg',
    children: [
      state.problem && { type: 'callout', tone: 'danger', title: 'The rules could not be changed', text: state.problem },
      {
        type: 'panel',
        title: 'Rule pack',
        subtitle: installed ? 'Installed. Edit or disable the rules on the Rules page.' : 'Not installed.',
        actions: [installed
          ? { type: 'button', text: 'Remove', action: 'remove', variant: 'danger' }
          : { type: 'button', text: 'Install', action: 'install', variant: 'primary' }],
        children: [{
          type: 'table',
          columns: [{ key: 'name', label: 'Rule' }, { key: 'state', label: 'State' }],
          rows: state.installed.map((rule) => ({ name: rule.name, state: rule.enabled ? 'enabled' : 'disabled by the operator' })),
          empty: 'None of the pack is installed.',
        }],
      },
      {
        type: 'panel',
        title: 'Uncovered failures',
        subtitle: 'Recent errors no custom retry rule handles. Details are already redacted.',
        children: [{
          type: 'table',
          columns: [{ key: 'status', label: 'Status' }, { key: 'detail', label: 'Detail' }, { key: 'count', label: 'Count', format: 'number' }],
          rows: state.errors.map((error) => ({ status: String(error.status || error.errorCode), detail: error.detail, count: error.count })),
          empty: 'No recent failures.',
        }],
      },
    ].filter(Boolean),
  }
}

async function show() {
  await refresh()
  await switchboard.ui.render(PAGE, view())
}

await switchboard.ui.registerPage({ id: PAGE, title: 'Rule pack', icon: 'shield' })

switchboard.ui.onEvent(PAGE, async (event) => {
  if (event.kind === 'action') {
    try {
      await (event.action === 'install' ? install() : remove())
    } catch (error) {
      state = { ...state, problem: error.message }
      await switchboard.ui.render(PAGE, view())
      return
    }
  }
  if (event.kind === 'show' || event.kind === 'action') await show()
})
