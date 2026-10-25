// Focus layout: hides and reorders built-in sidebar items.
//
// What it shows:
// - `ui.setLayout` (`ui:layout`): `hidden`, `order` and `startPage`;
// - `ui.resetLayout` from a palette command (`ui:commands`).
//
// The window keeps the operator in control: Plugins and Settings are never hidden (so the way back
// always stays on screen), only one plugin can hold the layout (`conflict`), the operator can reset it
// on the Plugins page, and it resets when the plugin stops and in safe mode.
const LAYOUTS = {
  operator: {
    order: ['overview', 'activity', 'providers', 'keys', 'rules'],
    hidden: ['tunnel', 'clients', 'appearance'],
    startPage: 'activity',
  },
  publisher: {
    order: ['tunnel', 'clients', 'overview', 'activity'],
    hidden: ['tests', 'insights'],
    startPage: 'tunnel',
  },
}

async function apply() {
  const layout = LAYOUTS[switchboard.settings.get('role')]
  try {
    await (layout ? switchboard.ui.setLayout(layout) : switchboard.ui.resetLayout())
  } catch (error) {
    // `conflict`: another plugin holds the layout. Say so instead of fighting over it.
    switchboard.log.warn(`The layout was not applied (${error.code ?? error.message}).`)
  }
}

await switchboard.commands.register({ id: 'reset', title: 'Focus layout: show every page' }, () => switchboard.ui.resetLayout())
await switchboard.commands.register({ id: 'apply', title: 'Focus layout: apply the role layout' }, apply)

switchboard.settings.onChange(apply)
await apply()
