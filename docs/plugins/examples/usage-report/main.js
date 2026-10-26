// Usage report: the analytics report as a page of tabs, charts and tables.
//
// What it shows:
// - `control.call('analytics.report', { period })` (`control:read`) feeding a page (`ui:pages`);
// - `tabs`, `segmented`, `select` and `slider` controls whose `change` events redraw the page;
// - `chart` (bars and a line), `table` with column formats, `kv`, `list`, `markdown` and `details`;
// - `files.save` (`files`) exporting the model table as CSV from a button, which is the gesture;
// - `ui.registerInspectorTab` (`ui:slots`): a Details tab in the Activity inspector that finds
//   the selected request through `activity.list` on its `show` event.
const PERIODS = [
  { value: '24h', label: '24 hours' },
  { value: '48h', label: '48 hours' },
  { value: '72h', label: '72 hours' },
  { value: 'all', label: 'All time' },
]
const SORTS = [
  { value: 'requests', label: 'Requests' },
  { value: 'p95Ms', label: 'p95 latency' },
  { value: 'tokensPerSecond', label: 'Tokens/s' },
]
const HELP = [
  'The report counts **finished requests** in the chosen period.',
  '',
  '- *Success* is completed requests over all finished ones.',
  '- *p50/p95* are latency percentiles from the first byte sent to the last byte received.',
  '- Models without a price in Insights are listed but count no cost.',
].join('\n')

await switchboard.ui.registerPage({ id: 'main', title: 'Usage report', icon: 'chart' })
const { surface: inspector } = await switchboard.ui.registerInspectorTab({ id: 'details', title: 'Details', scope: 'activity' })

const view = { tab: 'overview', period: '24h', sort: 'requests', top: 5 }
let report = null
let problem = ''

async function load() {
  try {
    report = await switchboard.control.call('analytics.report', { period: view.period })
    problem = ''
  } catch (error) {
    report = null
    problem = error.message
  }
}

const pct = (value) => `${Math.round((value ?? 0) * 1000) / 10}%`
const ms = (value) => (value >= 1000 ? `${(value / 1000).toFixed(1)} s` : `${Math.round(value ?? 0)} ms`)

function modelRows() {
  const key = view.sort
  return report.models
    .map((model) => ({
      model: model.model,
      provider: model.providerName,
      requests: model.volume.requests,
      success: model.volume.requests ? model.volume.completed / model.volume.requests : 0,
      p95Ms: model.p95Ms,
      tokensPerSecond: model.tokensPerSecond,
    }))
    .sort((a, b) => (key === 'p95Ms' ? b.p95Ms - a.p95Ms : b[key] - a[key]))
    .slice(0, view.top)
}

function overview() {
  const o = report.overview
  const days = report.daily.slice(-14)
  return [
    { type: 'metrics', items: [
      { label: 'Requests', value: o.volume.requests },
      { label: 'Success', value: pct(o.successRate), tone: o.successRate < 0.9 ? 'warning' : undefined },
      { label: 'p50 / p95', value: `${ms(o.p50Ms)} / ${ms(o.p95Ms)}` },
      { label: 'Tokens/s', value: o.tokensPerSecond },
    ] },
    { type: 'grid', columns: 2, children: [
      { type: 'chart', kind: 'bars', label: 'Requests per day', values: days.map((d) => d.volume.requests), labels: days.map((d) => d.date.slice(5)), height: 140 },
      { type: 'chart', kind: 'line', label: 'Success rate per day', values: days.map((d) => d.successRate), labels: days.map((d) => d.date.slice(5)), min: 0.8, max: 1, tone: 'success', height: 140 },
    ] },
    { type: 'details', summary: 'How these numbers are counted', children: [{ type: 'markdown', text: HELP }] },
  ]
}

function models() {
  return [
    { type: 'row', align: 'between', children: [
      { type: 'select', name: 'sort', label: 'Sort by', value: view.sort, options: SORTS },
      { type: 'slider', name: 'top', label: `Show top ${view.top}`, min: 1, max: 20, step: 1, value: view.top },
      { type: 'button', text: 'Export CSV…', action: 'export', icon: 'download', disabled: report.models.length === 0 },
    ] },
    { type: 'table', empty: 'No models in this period.', columns: [
      { key: 'model', label: 'Model', mono: true },
      { key: 'provider', label: 'Provider' },
      { key: 'requests', label: 'Requests', align: 'right', format: 'number' },
      { key: 'success', label: 'Success', align: 'right', format: 'percent' },
      { key: 'p95Ms', label: 'p95', align: 'right', format: 'duration' },
      { key: 'tokensPerSecond', label: 'Tokens/s', align: 'right', format: 'number' },
    ], rows: modelRows() },
    report.unpricedModels.length > 0 && { type: 'callout', tone: 'info', title: 'No price set', text: `${report.unpricedModels.join(', ')} count no cost. Add prices in Insights.` },
  ]
}

function errors() {
  return [
    { type: 'list', empty: 'No errors in this period.', items: report.errors.map((e) => ({ title: e.errorCode, meta: `${e.requests} requests`, tone: 'danger' })) },
    { type: 'heading', text: 'By provider', level: 3 },
    { type: 'kv', items: report.providers.map((p) => ({ key: p.name, value: p.errors.length ? p.errors.map((e) => `${e.errorCode} × ${e.requests}`).join(', ') : 'none' })) },
  ]
}

async function draw() {
  const header = { type: 'row', align: 'between', children: [
    { type: 'segmented', name: 'period', label: 'Period', value: view.period, options: PERIODS },
    { type: 'button', text: 'Refresh', action: 'refresh', variant: 'ghost', icon: 'refresh' },
  ] }
  if (!report) {
    await switchboard.ui.render('main', { type: 'stack', children: [header, { type: 'callout', tone: 'warning', title: 'The report is unavailable', text: problem || 'Try again in a moment.' }] })
    return
  }
  await switchboard.ui.render('main', { type: 'stack', children: [
    header,
    { type: 'tabs', name: 'tab', label: 'Report section', value: view.tab, tabs: [
      { id: 'overview', label: 'Overview', children: view.tab === 'overview' ? overview() : [] },
      { id: 'models', label: 'Models', children: view.tab === 'models' ? models() : [] },
      { id: 'errors', label: 'Errors', children: view.tab === 'errors' ? errors() : [] },
    ] },
    { type: 'text', text: `Generated ${new Date(report.generatedAt).toLocaleString()}`, tone: 'muted', size: 'sm' },
  ] })
}

// A cell holding a comma, quote or line break is quoted, with quotes doubled (RFC 4180).
const cell = (value) => (/[",\n\r]/.test(String(value)) ? `"${String(value).replaceAll('"', '""')}"` : String(value))

async function exportCsv() {
  const rows = modelRows()
  const lines = [['model', 'provider', 'requests', 'success', 'p95_ms', 'tokens_per_second'].join(',')]
  for (const r of rows) lines.push([r.model, r.provider, r.requests, r.success.toFixed(4), r.p95Ms, r.tokensPerSecond].map(cell).join(','))
  await switchboard.files.save({ name: `usage-${view.period}.csv`, text: `${lines.join('\n')}\n`, mime: 'text/csv' })
}

switchboard.ui.onEvent('main', async (event) => {
  if (event.kind === 'change' && event.name in view) {
    view[event.name] = event.name === 'top' ? Number(event.value) : String(event.value)
    if (event.name === 'period') await load()
  } else if (event.action === 'refresh' || (event.kind === 'show' && !report)) {
    await load()
  } else if (event.action === 'export' && report) {
    await exportCsv()
    return
  }
  await draw()
})

switchboard.ui.onEvent(inspector, async (event) => {
  if (event.kind !== 'show' || !event.context?.id) return
  let found
  try {
    const { requests } = await switchboard.control.call('activity.list')
    found = requests.find((request) => request.id === event.context.id)
  } catch {
    found = undefined
  }
  if (!found) {
    await switchboard.ui.render(inspector, { type: 'empty', title: 'Request not in the live list', hint: 'Only requests still in Activity have details here.' })
    return
  }
  await switchboard.ui.render(inspector, { type: 'kv', items: [
    { key: 'State', value: found.state },
    { key: 'Model', value: found.model },
    { key: 'Provider', value: found.providerName },
    { key: 'Status', value: found.status ?? '—' },
    { key: 'Queued', value: ms(found.queueMs) },
    { key: 'Latency', value: ms(found.latencyMs) },
    { key: 'Retries', value: found.retries },
    { key: 'Sent / received', value: `${found.bytesIn} / ${found.bytesOut} bytes` },
    found.errorCode && { key: 'Error', value: found.errorCode },
  ].filter(Boolean) })
})

await load()
await draw()
