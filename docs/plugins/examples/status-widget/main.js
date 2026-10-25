// Status widget: an Overview widget fed by `net.fetch` with a secret API key.
//
// What it shows:
// - `ui.registerWidget` (`ui:slots`) and `ui.render('widget:<id>', tree)`;
// - `net.fetch` (`net:fetch`) to a host listed in the package's `network.hosts`;
// - a `secret` setting: the script never sees its value, only its key in `settings.secrets`, and
//   writes `{{secret:apiKey}}` where the value belongs. Substitution happens in headers and the body,
//   never in the URL, and the value comes back as `[secret]` if the server echoes it;
// - an explicit state for every failure: no key, refused host, rate limit, network trouble.
//
// The API is fictional: GET https://status.example.com/v1/summary answers
// {"status":"operational","components":[{"name":"API","status":"operational"}, …]}.
const ENDPOINT = 'https://status.example.com/v1/summary'
const TONES = { operational: 'success', degraded: 'warning', partial_outage: 'warning', major_outage: 'danger' }

const { surface } = await switchboard.ui.registerWidget({ id: 'status', title: 'Upstream status', order: 10 })

let timer = 0

function render(tree) {
  return switchboard.ui.render(surface, tree)
}

function problem(title, text) {
  return render({ type: 'callout', tone: 'warning', title, text })
}

async function refresh() {
  // `settings.secrets` lists the secrets the operator has filled in: names only, never values.
  if (!switchboard.settings.secrets.includes('apiKey')) {
    return problem('No API key', 'Add the status API key in this plugin\'s settings on the Plugins page.')
  }
  let summary
  try {
    const response = await switchboard.net.fetch(ENDPOINT, { headers: { Authorization: 'Bearer {{secret:apiKey}}', Accept: 'application/json' }, timeoutMs: 10_000 })
    if (!response.ok) return problem('Status unavailable', `The status API answered ${response.status}.`)
    summary = response.json()
  } catch (error) {
    // Codes: not_allowed, blocked_address, missing_secret, rate_limited, too_large, timeout, network.
    const reason = {
      missing_secret: 'The API key was removed. Add it again in the plugin settings.',
      rate_limited: 'Too many requests; the next refresh will try again.',
      timeout: 'The status API did not answer in time.',
    }[error.code] ?? `The request failed (${error.code ?? 'unknown'}).`
    return problem('Status unavailable', reason)
  }
  const components = Array.isArray(summary?.components) ? summary.components.slice(0, 8) : []
  return render({
    type: 'stack',
    gap: 'sm',
    children: [
      { type: 'dot', tone: TONES[summary?.status] ?? 'neutral', text: String(summary?.status ?? 'unknown').replace(/_/gu, ' ') },
      { type: 'list', items: components.map((component) => ({ title: String(component.name), meta: String(component.status).replace(/_/gu, ' ') })), empty: 'No components reported.' },
    ],
  })
}

function schedule() {
  clearInterval(timer)
  const minutes = Number(switchboard.settings.get('minutes')) || 5
  timer = setInterval(() => void refresh(), minutes * 60_000)
  void refresh()
}

// Surfaces send `show` like pages do: refresh whenever the Overview comes back into view.
switchboard.ui.onEvent(surface, (event) => { if (event.kind === 'show') void refresh() })
switchboard.settings.onChange(schedule)
schedule()
