# Accessibility

## What this app commits to

Luxury Switchboard is a local operator tool, and operators use keyboards. The interface is built to be drivable without a mouse:

- Every control is a real button, link, or input; nothing needs a double click, and nothing important hides behind hover.
- Focus rings are visible everywhere (`--focus-ring`), and modals trap and restore focus.
- Tables support arrow-key row navigation; the settings rail and sidebars support Home/End and arrow keys.
- `prefers-reduced-motion` is honored: every animation the app has either stops or falls back to a static state.
- Live regions announce activity and test-run progress; the sidebar update pill is text, not color alone.
- Status is never color-only: dots carry labels, state pills carry words.

## Supported environments

Windows and Linux desktops, window sizes from 800x600 up. The dark palette is the only palette; contrast targets WCAG AA for text on its backgrounds, and several intentionally dimmed secondary elements (table headers, unit suffixes) sit near the AA boundary and are treated as known debt.

## Known limitations

- Screen reader support is uneven: the app uses standard widgets and aria labels, but it has not been audited with NVDA/JAWS/Orca, and several dense tables (Live Activity, Tests) convey part of their meaning through layout.
- The compact window layout (under 960 px) collapses the sidebar into a rail without a text alternative beyond tooltips.
- Charts (Insights daily usage) are decorative duplicates of tables: the exact numbers are always available in text form next to them.

## Reporting a barrier

Open an issue with the "accessibility" topic, or the word "a11y" in the title. Say what you were doing, what assistive tech you use, and what did not work. Accessibility bugs are treated as functional bugs, not polish: a control that cannot be reached by keyboard is a control that does not work.
