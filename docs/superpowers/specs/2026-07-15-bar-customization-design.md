# Bar customization + settings panel — design

Status: approved (2026-07-15). Implements user-configurable appearance for the
injected `<marquee-bar>`, both from the CLI and from an in-bar settings panel.

## Goal

Let a developer control where the bar sits, how big it is, its colour theme,
and which info pills show and in what order — first from CLI flags, then live
from a settings panel in the bar itself. Keep marquee's two hard promises: no
on-disk config file, and the accessibility guarantees in spec §3.4 (contrast,
keyboard, reduced-motion).

## Core model

- **CLI flags are the first-run defaults.** They flow `flags → status JSON →
  bar reads them as defaults`.
- **The panel wins and persists.** Panel changes are written to `localStorage`
  (keyed per proxied origin, so prefs are automatically per-app) and overlay the
  status defaults on every load, surviving marquee restarts.
- **Reset** clears the stored prefs, returning every knob to the CLI/flag
  default.
- Render order: `defaults (from status) → overlay stored prefs → render`.

This is not a config file: prefs live in the browser, per-app, and the CLI
remains the only server-side surface. The §7 anti-goal ("config files") stands.

## Knobs

| knob | CLI flag | values | panel control |
|---|---|---|---|
| position | `--position` | `bottom-left` (default), `bottom-right`, `top-left`, `top-right` | 4-corner picker (radiogroup) |
| size | `--size` | `small`, `medium` (default), `large` → scale factor | S / M / L buttons |
| theme | `--theme` | curated names, `default` (default) | `<select>`, a11y-verified palettes only |
| pills | `--pills` | ordered CSV, default `branch,dirty,worktree,pr`; omission = hidden | checkbox + ↑/↓ per pill |

- `--pills` encodes **order and visibility in one flag**: list order = render
  order, an omitted pill is hidden. Unknown pill names are a flag error.
- The **branch chip stays auto-hash-coloured** (FNV-1a hue + contrast-picked
  text) regardless of theme — themes never touch it, so the contrast guarantee
  is structurally preserved.
- The **switcher** (worktree dropdown), **collapse toggle**, and the new
  **gear** are fixed controls, not reorderable pills.

### Positions

`data-position` on `:host` selects the corner; CSS anchors top/bottom + left/right
accordingly. The worktree menu **and** the settings popover flip their horizontal
anchor (`left: 0` ↔ `right: 0`) and vertical anchor (open up from a bottom corner,
down from a top corner) to stay on-screen.

### Sizes

`data-size` maps to a scale factor applied through a single `--mq-scale` custom
property that multiplies height, font-size, padding, and radius. `small` ≈ 0.85,
`medium` = 1.0, `large` ≈ 1.2 (exact values tuned during implementation against
screenshots). The corner offset stays a fixed 8px.

### Themes

Each theme is a set of CSS custom properties on `:host` (`--mq-bg`, `--mq-fg`,
`--mq-border`, `--mq-chip-bg`) selected by `data-theme`. Curated set (final names
tuned in implementation, all verified ≥4.5:1 in both `prefers-color-scheme`
modes): `default` (current stone/neutral), a high-contrast option, and one or two
extra palettes. Themes restyle the bar chrome and read-only chips only; the
branch chip and dirty/PR semantics are untouched.

### Pills

The four info pills — `branch`, `dirty`, `worktree`, `pr` — render from an
ordered, visibility-aware list rather than fixed DOM order. `worktree` still
self-hides when on the main worktree; `pr` still self-hides when there is no PR;
`dirty` still self-hides when clean. A pill the user hides stays hidden even when
its data is present.

## Settings panel

- A **⚙ button** on the bar opens a **popover** anchored to the bar, using the
  same disclosure pattern as the worktree menu.
- Contents top-to-bottom: Position (4-corner radiogroup), Size (S/M/L), Theme
  (`<select>`), Pills (list; each row = checkbox + label + ↑/↓), **Reset**.
- Changes apply **live** and persist immediately.

## Accessibility (spec §3.4 — acceptance criteria, not polish)

- ⚙ is a real `<button>` with an accessible name; the panel is a disclosure:
  Escape closes and returns focus to ⚙, focus is trapped sensibly, all controls
  reachable and operable by keyboard.
- Position picker is a `radiogroup` with four `radio`s; Size is a group of
  toggle buttons with `aria-pressed`; Theme is a native `<select>`; each pill row
  has a labelled checkbox and labelled ↑/↓ buttons (disabled at the ends).
- Every curated theme is verified ≥4.5:1 text contrast in light and dark.
- `prefers-reduced-motion` continues to disable the switch spinner and any panel
  transition.
- Bar keeps `role="status"`; a position/size/theme/pill change is a visual-only
  restyle of the same content, so no false live-region announcements are forced.

## Code structure

`bar.js` is already 600 lines; adding the panel here would push it past 1000 —
too much for one unit. Split at the natural seam into embedded ES modules
(`//go:embed *.js`; the proxy serves each sibling same-origin, already covered by
the `'self'` CSP relaxation):

- **`prefs.js`** — pure, no DOM: the defaults tables (positions, sizes, themes,
  pill ids) and `load` / `merge(defaults, stored)` / `save` / `reset` /
  `validate` against `localStorage`. Independently testable.
- **`settings.js`** — the ⚙ popover: builds the panel DOM, wires the controls,
  and calls back into the element to apply + persist. No status/network concerns.
- **`bar.js`** — the custom element: polling, order/visibility-aware pill
  rendering, the switcher, and hosting the settings panel.

The JS module split and file boundaries are performed with the
`improve-codebase-architecture` skill so the seams are clean, not incidental.

## Two UX fixes folded in (same subsystem)

1. **Control affordance:** the switcher and gear currently read like read-only
   chips (transparent border). Give them a real border + affordance glyph
   (`▾` / ⚙) so they read as controls.
2. **PR-chip layout shift:** the PR chip loads asynchronously and shoves the bar
   sideways when it appears. Reserve its space (skeleton placeholder) so the bar
   stops jumping.

## Testing

- Go: `options_test` covers the new flags and their validation (bad corner, bad
  size, unknown theme, unknown pill, malformed `--pills`); `status_test` covers
  the new fields in the payload.
- `e2e_test.go` (headless browser) extended: open the panel; change position,
  size, theme, and pill order/visibility; **reload and confirm prefs persist**;
  Reset returns to defaults. Keyboard-only path for the panel is exercised.
- Every UI-affecting PR includes before/after screenshots.

## Delivery — atomic PRs

One concern per PR (AGENTS: one task = one PR). Exact plan produced by
`writing-plans`; expected sequence:

1. `fix:` PR-chip layout shift (reserve space).
2. `feat:` `--position` four corners (flag + status + CSS + menu/anchor flip).
3. `refactor:`/`feat:` prefs foundation + `prefs.js`/`settings.js` split + ⚙
   popover shell wired to Position end-to-end (the architecture PR).
4. `feat:` size presets (`--size` + panel).
5. `feat:` curated themes (`--theme` + panel).
6. `feat:` pills show/hide + reorder (`--pills` + panel), incl. control
   affordance polish.

## Out of scope (roadmap, not this iteration)

- Free colour pickers / per-branch colour override (breaks the contrast
  guarantee).
- Free numeric size slider.
- Any on-disk config file.
