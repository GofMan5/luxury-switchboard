# Design system — "instrument panel"

The interface is a dense, calm instrument panel: cool graphite surfaces, hairlines instead of boxes, monospace only for data, one accent signal. Every visual value reads a token from `frontend/src/index.css`. Themes, the Appearance page, plugin themes and user CSS all rewrite those tokens at runtime, so **a hardcoded colour, size or radius is a bug**: it will not follow the theme (there is a light theme, Paper/Porcelain), density or radius setting.

## Tokens (public API — use these, never literals)

| Purpose | Tokens |
| --- | --- |
| Surfaces | `--bg-chrome` (frame) · `--bg-canvas` (workspace) · `--bg-surface` (panels, tables, dialogs) · `--bg-elevated` (popovers, menus, toasts) · `--bg-raised` (inset wells, code, previews) · `--bg-hover` · `--bg-selected` · `--bg-nav-selected` · `--bg-field` (inputs) |
| Lines | `--border-subtle` (hairlines, panel borders) · `--border-strong` (secondary buttons, separators that must read) · `--border-field` (inputs at rest) |
| Text | `--text-primary` (headings, values) · `--text-secondary` (body, cells) · `--text-muted` (labels, captions, column headers) · `--text-disabled` · `--text-danger` |
| Accent | `--accent` · `--accent-soft` (wash) · `--accent-line` (accent hairline) · `--action-primary/-hover/-pressed` · `--text-on-action` · `--focus-ring` · `--selection` |
| Status | `--status-info` · `--status-success` · `--status-warning` · `--status-danger` · `--status-neutral` |
| Effects | `--shadow-float` (dialogs/popovers/toasts only) · `--scrim` (modal backdrop) |
| Type | `--font-ui` · `--font-mono` · sizes `--fs-xs` 11 · `--fs-sm` 12 · `--fs-base` 13 · `--fs-md` 14 · `--fs-lg` 16 · `--fs-xl` 18 · `--fs-2xl` 20 (px at the default text size; they scale) |
| Density | `--row-height` 34 · `--control-height` 30 · `--control-height-sm` 24 · `--nav-item` 30 · `--gutter` 24 (page side padding) · `--gap` 16 (between panels) · `--pad` 14 (inside panels) |
| Shape | `--radius-control` 6 · `--radius-panel` 8 · `--radius-modal` 12 |
| Motion | `--motion-fast` 120ms · `--motion-standard` 180ms · `--ease-standard` |

Tints of a status colour: `color-mix(in srgb, var(--status-warning) 8%, transparent)` for a wash, 30–40% for its border. Never invent a hex.

## Rules

1. **No literal colours.** No `#hex`, `rgb()`, `rgba()`, `hsl()` or named colours in feature CSS, except `transparent`, `currentColor` and `inherit`. Map greys to the nearest surface/text/border token. Map blues/greens/ambers/reds to the status tokens.
2. **Type scale only.** `font-size` uses `var(--fs-*)`. Page h1 is styled globally (`.page-header h1`); panel titles are `--fs-md` 600; section labels `--fs-xs`/`--fs-sm` 500 muted; body `--fs-base`; captions `--fs-sm`/`--fs-xs`. Drop per-page h1 overrides. Large readouts: `--fs-2xl` max, mono, `font-variant-numeric: tabular-nums`.
3. **Mono only for data**: numbers in tables and readouts, addresses, ids, model names in tables, hashes, code. Labels and prose stay in the UI font. Every numeric column gets `font-variant-numeric: tabular-nums`.
4. **Density.** Rows `min-height`/`height: var(--row-height)`; controls `var(--control-height)` (`-sm` for inline/table controls); panel inner padding `var(--pad)`; page padding `var(--gutter)`; gaps between panels `var(--gap)`. Compact is the norm: headers ~44px, table headers ~30px, no 60–72px bars, no 20–28px paddings inside panels.
5. **Radii** use the three radius tokens (or `calc()` of them); round pills/dots may use `999px`/`50%`.
6. **Elevation**: either a 1px `--border-subtle` border or `--shadow-float`, never both on the same element, and shadows only on floating layers. No glow, no `box-shadow` halos, no gradients, no `backdrop-filter` blur, no coloured `border-left` thicker than 1px (use a 2px top tick, a dot, or a pill for state).
7. **States** for every interactive element: `:hover` (`--bg-hover` / text to primary), `:active` (`--bg-selected`), `:focus-visible` (`outline: 2px solid var(--focus-ring)`; offset 2px, or -2px for full-bleed rows), `:disabled` (`--text-disabled`, `cursor: not-allowed`). Transitions: `color`, `background`, `border-color`, `opacity`, `transform` only, using `var(--motion-fast)`/`var(--motion-standard)`.
8. **Selected/active** items: `--bg-selected` plus `--text-primary`; an accent marker may use `--accent` (icon colour, a 2px tick, a dot). The primary button is the only accent fill.
9. **Global classes are styled globally**: `.button` (+ `-primary/-secondary/-danger/-ghost`), `.page-header`, `.page-body`, `.ui-modal`, `.ui-scrim`, `.status-dot`, `.sr-only`, `kbd`, `code`, inputs/selects/textareas. Feature CSS adjusts layout around them, never re-skins them with literals. Prefer deleting an override that only fought the old look.
10. **Layout safety**: supported from 800×600. Keep `min-width: 0` on flex/grid children, ellipsis for long single-line text, no horizontal overflow of the primary workflow; wide tables scroll inside their panel. Keep every existing media query breakpoint behaviour (1279/1099/959/600) unless it is replaced by something equivalent.
11. **Do not touch** class names used by TSX, `data-*` selectors, `:global(...)` names, or any TSX/TS file. CSS-only changes. Do not delete a class that TSX references (grep the sibling `.tsx` when unsure).
12. Motion respects `prefers-reduced-motion` and `[data-animations='off']` (already handled globally for animations/transitions; do not add infinite animations other than existing spinners/live pulses).
