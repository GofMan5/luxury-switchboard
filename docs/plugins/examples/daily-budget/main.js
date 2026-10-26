// Daily budget: today's finished requests against a limit the operator sets.
//
// What it shows:
// - `events.on('activity.changed')` (`control:read`): each request is counted once, when it
//   reaches a final state; the count starts again at local midnight;
// - `ui.setStatus` (`ui:status`): a readout in the runtime bar whose tone follows the count;
// - `notify` (`ui:notify`): once at the warning level and once at the limit, per day;
// - `bus.publish` (`bus`): a `budget.alert` other plugins can subscribe to without this
//   plugin's permissions;
// - `ui.registerSettingsSection` (`ui:slots`) with metrics, a progress bar and a reset button
//   behind `ui.confirm`; `settings.onChange` redraws when the limits change;
// - `storage` keeping the day's count across restarts, written at most every few seconds,
//   well inside the 120-writes-a-minute budget.
const FINAL = new Set(['completed', 'failed', 'cancelled'])
const SAVE_EVERY_MS = 5000
const MAX_SEEN = 2000 // ids remembered so a request is never counted twice

const { surface } = await switchboard.ui.registerSettingsSection({
  id: 'budget',
  title: 'Daily budget',
  description: 'Finished requests today against the limit set in this plugin\'s settings.',
})

const today = () => new Date().toLocaleDateString('sv') // YYYY-MM-DD in local time
const fresh = () => ({ date: today(), count: 0, warned: false, reached: false })
let day = { ...fresh(), ...JSON.parse((await switchboard.storage.get('day')) ?? '{}') }
const seen = new Set()
let saveTimer = null

function limits() {
  const limit = Math.max(1, Number(switchboard.settings.get('limit')) || 500)
  const warnAt = Math.ceil(limit * (Number(switchboard.settings.get('warnAt')) || 80) / 100)
  return { limit, warnAt }
}

function save() {
  if (saveTimer) return
  saveTimer = setTimeout(async () => {
    saveTimer = null
    await switchboard.storage.set('day', JSON.stringify(day))
  }, SAVE_EVERY_MS)
}

function rollOver() {
  if (day.date === today()) return
  day = fresh()
  seen.clear()
  save()
}

async function alert(level, title, body) {
  const { limit } = limits()
  await switchboard.notify({ title, body, tone: level === 'limit' ? 'danger' : 'warning' })
  await switchboard.bus.publish('budget.alert', { level, count: day.count, limit, date: day.date })
}

async function check() {
  const { limit, warnAt } = limits()
  if (!day.warned && day.count >= warnAt && day.count < limit) {
    day.warned = true
    await alert('warning', 'Daily budget almost used', `${day.count} of ${limit} requests today.`)
  }
  if (!day.reached && day.count >= limit) {
    day.reached = day.warned = true
    await alert('limit', 'Daily budget reached', `${day.count} requests today; the limit is ${limit}. Requests are not blocked.`)
  }
}

async function draw() {
  const { limit, warnAt } = limits()
  const tone = day.count >= limit ? 'danger' : day.count >= warnAt ? 'warning' : 'success'
  await switchboard.ui.setStatus('budget', {
    text: `Budget ${day.count}/${limit}`,
    tone,
    tooltip: `Finished requests today (${day.date}). Warns at ${warnAt}.`,
  })
  await switchboard.ui.render(surface, {
    type: 'stack',
    children: [
      { type: 'metrics', items: [
        { label: 'Today', value: day.count, detail: day.date },
        { label: 'Limit', value: limit, detail: `warns at ${warnAt}` },
        { label: 'Left', value: Math.max(0, limit - day.count), tone: tone === 'success' ? undefined : tone },
      ] },
      { type: 'progress', value: day.count / limit, label: `${Math.round(day.count / limit * 100)}% of today's budget`, tone },
      { type: 'row', align: 'between', children: [
        { type: 'text', text: 'Change the limit and the warning level in this plugin\'s settings.', tone: 'muted', size: 'sm' },
        { type: 'button', text: 'Reset count', action: 'reset', variant: 'danger', disabled: day.count === 0 },
      ] },
    ],
  })
}

await switchboard.events.on('activity.changed', async (request) => {
  rollOver()
  if (!FINAL.has(request?.state) || seen.has(request.id)) return
  seen.add(request.id)
  if (seen.size > MAX_SEEN) seen.delete(seen.values().next().value) // a Set iterates oldest first
  day.count += 1
  save()
  await check()
  await draw()
})

switchboard.ui.onEvent(surface, async (event) => {
  if (event.kind === 'show') rollOver()
  if (event.action === 'reset') {
    const yes = await switchboard.ui.confirm({
      title: 'Reset today\'s count?',
      body: `${day.count} requests counted today will start again from zero.`,
      confirmText: 'Reset',
      tone: 'danger',
    })
    if (!yes) return
    day = fresh()
    await switchboard.storage.set('day', JSON.stringify(day))
  }
  await draw()
})

switchboard.settings.onChange(async () => {
  await check()
  await draw()
})

// Midnight can pass while nothing happens; check once a minute so the readout starts over.
setInterval(() => {
  const before = day.date
  rollOver()
  if (day.date !== before) void draw()
}, 60_000)

rollOver()
await draw()
