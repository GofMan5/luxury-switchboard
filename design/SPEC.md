# Switchboard design specification

The source concepts are in `design/concepts/`:

- `switchboard-overview-1440x900.png` — standard/wide Overview and request inspector.
- `switchboard-overview-compact.png` — compact navigation rail and reduced activity columns.
- `switchboard-providers-1440x900.png` — provider editing and route preview workflow.

## Visual system

- Modern Codex/ChatGPT-inspired neutral dark system: chrome `#101010`, canvas `#181818`, surface `#1f1f1f`, elevated controls `#242424`, selected navigation `#262626`, subtle blue only for selection and focus. It borrows the calm spacing and typography, not branded AI decoration.
- Primary text `#f2f2f2`, secondary `#c7c7c7`, muted `#8c8c8c`, focus `#7ca7d2`.
- Semantic status is invariant: completed `#5dbb67`, running `#4388e8`, queued/retrying `#e4b24d`, failed `#e7655b`, paused `#929aa3`.
- The generated compact concept accidentally colors some state dots inconsistently. The invariant above is authoritative and must be enforced by code and visual tests.
- No gradients, glow, glass, decorative pills, nested card grids or AI imagery.

## Typography and density

- UI font: `Segoe UI Variable`, `Segoe UI`, system sans-serif.
- Numeric columns use tabular numerals. Body/control text 13px, table header 12px/600, page heading 25px/650.
- Standard row height 42px; interactive controls use 36-39px visual height with at least 42px navigation targets.
- One-pixel dividers establish hierarchy. Controls use 8px radii; large bounded panes use 12px.

## Responsive contract

- Wide `>= 1280px`: 222px navigation, activity table and 360px inspector side by side.
- Standard `960-1279px`: 204px navigation, inspector becomes an overlay drawer before the table loses its usable width.
- Compact `< 960px`: 56px icon rail, inspector drawer, summary reduced to RPM/active/queue/p95, secondary table columns hidden by priority.
- Runtime chrome is 58px wide/standard and 56px compact. Compact navigation removes vertical gaps before allowing an internal scrollbar.
- Default desktop window is 1440x900; minimum supported window is 800x600. Primary workflows must not require page-level horizontal scrolling.

## Interaction contract

- Every state has hover, focus-visible, pressed, disabled, loading and error treatment.
- A row opens detail with single selection plus an explicit Details action; double click is an accelerator, never the only path.
- Live tables preserve selection by request id, keep a bounded 100-row render window and announce connection changes without stealing focus. Add virtualization only if that bounded window is intentionally raised.
- Motion is limited to 120-180ms state transitions and respects `prefers-reduced-motion`.
