// A page plugin: register a page, draw it as a tree of standard nodes, redraw
// whenever something changes. The window renders the tree with its own
// components; a plugin never touches the DOM.
const PAGE = 'main';
let count = Number(await switchboard.storage.get('count')) || 0;

await switchboard.ui.registerPage({ id: PAGE, title: switchboard.plugin.name, icon: 'puzzle' });

function view() {
  return {
    type: 'stack',
    gap: 'md',
    children: [
      { type: 'heading', text: switchboard.settings.get('greeting'), level: 1 },
      { type: 'text', tone: 'muted', text: 'Change the greeting in Plugins → ' + switchboard.plugin.name + ' → Settings.' },
      { type: 'panel', title: 'Counter', subtitle: 'Kept in plugin storage', children: [
        { type: 'metric', label: 'Clicks', value: count, detail: 'since install' },
        { type: 'row', gap: 'sm', children: [
          { type: 'button', text: 'Add one', action: 'add', variant: 'primary' },
          { type: 'button', text: 'Reset', action: 'reset', disabled: count === 0 },
        ] },
      ] },
    ],
  };
}

async function draw() {
  await switchboard.ui.render(PAGE, view());
}

switchboard.ui.onEvent(PAGE, async (event) => {
  if (event.action === 'add') count += 1;
  else if (event.action === 'reset') count = 0;
  else return;
  await switchboard.storage.set('count', String(count));
  await draw();
});

switchboard.settings.onChange(() => draw());

await draw();
