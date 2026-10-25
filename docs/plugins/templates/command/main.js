// A command plugin: actions in the command palette (Ctrl+K). A handler runs
// only when the operator picks it, which is also what lets it write to the
// clipboard or ask a question. The palette lists them under the plugin's name.
await switchboard.commands.register({
  id: 'hello',
  title: 'Say hello',
  keywords: ['hello', 'greet'],
}, async () => {
  await switchboard.notify({ title: 'Hello', body: 'From ' + switchboard.plugin.name + ' ' + switchboard.plugin.version + '.', tone: 'info' });
});

await switchboard.commands.register({
  id: 'copy-time',
  title: 'Copy the current time',
  keywords: ['time', 'timestamp', 'clipboard'],
}, async () => {
  const now = new Date().toISOString();
  await switchboard.clipboard.writeText(now);
  await switchboard.notify({ title: 'Copied', body: now, tone: 'success' });
});
