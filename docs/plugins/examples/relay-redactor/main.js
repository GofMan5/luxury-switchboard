// Prompt redactor: a request hook that rewrites prompts before routing.
//
// What it shows:
// - `relay.onRequest` (`relay:content`), filtered to the chat-style endpoints;
// - a `rewrite` decision that replaces the body and tags the activity record;
// - `relay.onResponse`, an observer that sees the outcome but cannot change it;
// - logging counts only: prompts and answers never belong in the plugin log.
//
// Hooks see the local relay only, never the public tunnel or model tests. A hook that throws or
// runs past `timeoutMs` lets the request continue unchanged (fail-open), so this is a convenience
// for your own clients, not a data-loss barrier.
const EMAIL = /[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}/gu
// Seven or more digits with optional +, spaces, dots, dashes or brackets between them.
const PHONE = /\+?\d[\d ().-]{5,}\d/gu

let redacted = 0

/** Replaces matches in every string value of the JSON body, keys untouched. */
function redact(body) {
  let changes = 0
  const replace = (text, pattern, label) => text.replace(pattern, () => { changes += 1; return `[${label}]` })
  const phones = switchboard.settings.get('phones')
  const value = JSON.parse(body, (_key, item) => {
    if (typeof item !== 'string') return item
    const masked = replace(item, EMAIL, 'email')
    return phones ? replace(masked, PHONE, 'phone') : masked
  })
  return { changes, body: JSON.stringify(value) }
}

await switchboard.relay.onRequest({ endpoints: ['chat', 'responses', 'messages'], timeoutMs: 500 }, (request) => {
  // A body over 192 KiB arrives as `bodyOmitted` and cannot be rewritten.
  if (request.body === undefined) return { action: 'continue' }
  const result = redact(request.body)
  if (result.changes === 0) return { action: 'continue' }
  redacted += result.changes
  return { action: 'rewrite', body: result.body, tags: ['redacted'] }
})

await switchboard.relay.onResponse({ endpoints: ['chat', 'responses', 'messages'] }, (request, response) => {
  if (response.status >= 400) switchboard.log.warn(`${request.model} answered ${response.status}${response.errorCode ? ` (${response.errorCode})` : ''}.`)
})

switchboard.onActivate(() => switchboard.log.info('Redacting e-mail addresses' + (switchboard.settings.get('phones') ? ' and phone numbers.' : '.')))
// Counts only; never the values themselves.
setInterval(() => { if (redacted > 0) { switchboard.log.info(`Redacted ${redacted} values.`); redacted = 0 } }, 60_000)
