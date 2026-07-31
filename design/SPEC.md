# Switchboard design specification

The source concepts are in `design/concepts/`:

- `switchboard-overview-1440x900.png` — standard/wide Overview and request inspector.
- `switchboard-overview-compact.png` — compact navigation rail and reduced activity columns.
- `switchboard-providers-1440x900.png` — provider editing and route preview workflow.

## Visual system

- Modern Codex/ChatGPT-inspired neutral dark system: chrome `#171717`, canvas `#212121`, surface `#262626`, raised/selected navigation `#2f2f2f`, subtle blue only for data-row selection and focus. It borrows the calm spacing and typography, not branded AI decoration.
- Primary text `#f2f4f5`, secondary `#b2b7bc`, muted `#7f878e`, focus/selection `#3d8df5`.
- Semantic status is invariant: completed `#52c878`, running `#4594f7`, queued/retrying `#e5ad3f`, failed `#ef5a55`, paused `#89919a`.
- The generated compact concept accidentally colors some state dots inconsistently. The invariant above is authoritative and must be enforced by code and visual tests.
- No gradients, glow, glass, decorative pills, nested card grids or AI imagery.

## Typography and density

- UI font: Inter Variable where bundled, then `Segoe UI Variable`, `Segoe UI`, sans-serif.
- Numeric columns use tabular numerals. Body/control text 13px, table header 12px/600, page heading 25px/650.
- Standard row height 40px; interactive controls at least 38px; critical compact touch/click targets at least 42px.
- One-pixel dividers establish hierarchy. Radius is 4px for inputs/buttons and 6px only for large bounded panes.

## Responsive contract

- Wide `>= 1280px`: 230px navigation, activity table and 340px inspector side by side.
- Standard `960-1279px`: 196px navigation, inspector becomes an overlay drawer when table width would fall below 720px.
- Compact `< 960px`: 56px icon rail, inspector drawer, summary reduced to RPM/active/queue/p95, secondary table columns hidden by priority.
- Default desktop window is 1440x900; minimum supported window is 800x600. Primary workflows must not require page-level horizontal scrolling.

## Interaction contract

- Every state has hover, focus-visible, pressed, disabled, loading and error treatment.
- A row opens detail with single selection plus an explicit Details action; double click is an accelerator, never the only path.
- Live tables preserve selection by request id, virtualize beyond the visible window and announce connection changes without stealing focus.
- Motion is limited to 120-180ms state transitions and respects `prefers-reduced-motion`.
