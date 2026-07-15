# Bar customization + settings panel — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Each task = one atomic PR.

**Goal:** Make the injected `<marquee-bar>` configurable — corner position, size preset, curated theme, and pill show/hide + reorder — from both CLI flags and an in-bar settings panel, prefs persisting per-app in `localStorage`.

**Architecture:** CLI flags set defaults that flow through the status JSON; the bar overlays `localStorage` prefs on top and renders. The embedded bar asset is split into three ES modules (`prefs.js` pure core, `settings.js` panel UI, `bar.js` element) served same-origin under the existing `'self'` CSP relaxation.

**Tech Stack:** Go stdlib only; vanilla ES modules in shadow DOM; Go `e2e_test.go` headless-browser tests.

## Global Constraints

- Go stdlib only — no new dependencies.
- `go test ./...` and `golangci-lint run` pass locally before every PR.
- Fail-open: no bar/config error may break the proxied app response; a bad/absent pref falls back to the default, never throws.
- Accessibility is acceptance criteria (spec §3.4): keyboard-operable, contrast ≥4.5:1 both schemes, `prefers-reduced-motion` honored, real semantics.
- IP hygiene: fixtures synthetic, no employer material.
- One task = one atomic PR; conventional commits; never reference AI tooling.
- Reference spec: `docs/superpowers/specs/2026-07-15-bar-customization-design.md`.
- Every UI-affecting PR is verified live in a real headless browser (see Testing strategy).

## Testing strategy (READ THIS — supersedes any per-task "e2e_test.go" wording)

marquee is **Go-stdlib-only**, so there is **no committed browser test** for the injected bar JS and **no `internal/bar/embed_test.go` (markers; browser check via MCP — see Testing strategy)** (it never existed; the only Go e2e harness, `e2e/e2e_test.go`, is HTTP-only and cannot measure DOM layout). A real DOM/layout test needs CDP-over-websocket → a forbidden dependency. So, for every task below, "e2e"/"browser test" means this three-part strategy, NOT a new Go browser test:

1. **Go tests (real CI coverage)** for the Go side: `cmd/marquee/options_test.go` (flag parsing/validation) and `internal/status/status_test.go` (payload fields).
2. **`internal/bar/embed_test.go` structural markers** — assert the expected strings/behaviors are present in the embedded JS asset, pinning them into `go test`.
3. **Live verification in a real headless browser via the Playwright/chrome-devtools MCP** — reproduce fail-first on `main` where applicable, confirm the fix passes, capture bounding-box/interaction measurements and screenshots (to scratchpad; report them in the PR body as measurements + description, per the #29/#30/#34 precedent). This is per-PR verification, not committed CI.

Do not attempt to create or extend `internal/bar/embed_test.go` (markers; browser check via MCP — see Testing strategy).

## File map

- `internal/bar/bar.js` — element: polling, order/visibility-aware pill render, switcher, hosts panel. (modify; will shrink as modules split out)
- `internal/bar/prefs.js` — **new**, pure: defaults tables + load/merge/save/reset/validate on `localStorage`.
- `internal/bar/settings.js` — **new**: the ⚙ popover panel DOM + wiring.
- `internal/bar/embed.go` — switch `//go:embed bar.js` → `//go:embed *.js`; serve siblings.
- `internal/proxy/*` (asset serving) — serve `/__marquee/prefs.js`, `/__marquee/settings.js` with `text/javascript`.
- `cmd/marquee/options.go` — new flags `--size`, `--theme`, `--pills`; extend `--position` validation.
- `internal/status/status.go` — new payload fields: `size`, `theme`, `pills` (ordered), keep `position`.
- `cmd/marquee/main.go`, `cmd/marquee/attach.go` — wire new opts into the status deps.
- Tests: `cmd/marquee/options_test.go`, `internal/status/status_test.go`, `internal/bar/embed_test.go` (markers; browser check via MCP — see Testing strategy).

---

### Task 1 (PR 1) — fix: reserve PR-chip space (layout shift)

**Files:** Modify `internal/bar/bar.js` (CSS + `#render`); Test `internal/bar/embed_test.go` (markers; browser check via MCP — see Testing strategy).

**Problem:** `.pr` chip is `hidden` until the async GH poll resolves, then appears and shoves the switcher sideways.

- [ ] Write an e2e assertion: after first paint (status without PR), the bounding-x of the switcher/toggle does not move once a PR later arrives. (Drive with the test upstream returning a PR on the 2nd poll.)
- [ ] Verify it fails.
- [ ] Fix: keep the `.pr` slot occupying space while pending — render a `.pr` skeleton (fixed min-width, shimmer that respects `prefers-reduced-motion`) instead of `hidden`, until `status.pr` is known-absent vs known-present. When the poll has resolved and there is genuinely no PR, collapse the slot (no layout jump because switcher sits right of a now-stable region — confirm with the test).
- [ ] Verify it passes; screenshots (pending vs resolved).
- [ ] Commit: `fix: Reserve the PR chip's space to stop the bar shifting on load`.

**Note:** if reserving proves visually worse than reordering, the fallback (switcher leftmost) is acceptable — but prefer reserve; document the choice in the PR body.

---

### Task 2 (PR 2) — feat: four-corner `--position`

**Files:** Modify `cmd/marquee/options.go`, `internal/status/status.go` (already carries `position`), `internal/bar/bar.js` (CSS + anchor logic); Test `cmd/marquee/options_test.go`, `internal/bar/embed_test.go` (markers; browser check via MCP — see Testing strategy).

**Interfaces produced:** flag accepts `bottom-left|bottom-right|top-left|top-right`; status `position` carries the same string; bar sets `data-position` (replacing the current top/bottom-only attr).

- [ ] `options_test`: `--position bottom-right` accepted; `--position top-left` accepted; `--position sideways` errors with a message listing the four values. Default is `bottom-left`.
- [ ] Verify fails.
- [ ] `options.go`: change default `"bottom"→"bottom-left"`, validation set to the four corners, update usage string. Do this for both run and attach flag sets.
- [ ] `bar.js`: replace `:host([position="top"])` handling with `data-position` on `:host`; CSS anchors top/bottom + left/right per corner; the worktree menu and (future) panel flip horizontal anchor (`left:0`↔`right:0`) and vertical direction by corner.
- [ ] e2e: for each corner, assert the host computed `top/bottom/left/right` and that the worktree menu opens on-screen (not clipped).
- [ ] Verify passes; screenshots of all four corners.
- [ ] Commit: `feat: Let --position place the bar in any of the four corners`.

---

### Task 3 (PR 3) — refactor+feat: prefs core, module split, ⚙ popover wired to Position

This is the architecture PR. **Use the `improve-codebase-architecture` skill** to draw the `prefs.js` / `settings.js` / `bar.js` seams cleanly before/while implementing.

**Files:** Create `internal/bar/prefs.js`, `internal/bar/settings.js`; modify `internal/bar/bar.js`, `internal/bar/embed.go`, proxy asset serving; Test `internal/bar/embed_test.go` (markers; browser check via MCP — see Testing strategy), `internal/bar/embed_test.go`.

**`prefs.js` interface (pure, produced for later tasks):**
- `DEFAULTS` — `{ position, size, theme, pills }` shape mirror of status defaults.
- `load(storage)` → stored prefs object or `{}` (never throws).
- `merge(defaults, stored)` → effective prefs (stored keys win; unknown/invalid values dropped in favor of default).
- `save(storage, prefs)`, `reset(storage)`.
- `validate(prefs, defaults)` → sanitized prefs.

- [ ] `prefs.js` with the pure functions above; e2e (or a tiny in-browser eval harness in e2e_test) asserts merge/validate/reset behavior including malformed JSON → `{}`.
- [ ] `embed.go`: `//go:embed *.js`; `embed_test` asserts all three assets present.
- [ ] Proxy serves `/__marquee/prefs.js` and `/__marquee/settings.js` (`text/javascript`); `bar.js` imports them.
- [ ] `settings.js`: builds the ⚙ popover (disclosure: Escape closes → focus returns to ⚙); Position radiogroup only, for now. Applies live + persists via `prefs.save`.
- [ ] `bar.js`: reads defaults from status, overlays `load`+`merge`, renders; adds the ⚙ button; delegates panel to `settings.js`.
- [ ] e2e: open panel via keyboard, change position, reload → persists; Reset → back to CLI default; panel a11y (focus return, Escape).
- [ ] Screenshots (panel open, each position via panel).
- [ ] Commit(s): `refactor: Split the bar asset into prefs, settings, and bar modules` + `feat: Add the settings panel with a persisted position control`. (Two commits, one PR — the split is a distinct concern from the new panel.)

---

### Task 4 (PR 4) — feat: size presets

**Files:** Modify `cmd/marquee/options.go`, `internal/status/status.go`, `internal/bar/bar.js` (CSS `--mq-scale`), `internal/bar/settings.js`, `internal/bar/prefs.js` (DEFAULTS.size); Test `options_test`, `status_test`, `e2e_test`.

**Interface:** `--size small|medium|large` (default `medium`); status `size`; bar sets `data-size`; CSS maps to `--mq-scale` (~0.85/1.0/1.2, tuned to screenshots) multiplying height/font/padding/radius.

- [ ] `options_test`/`status_test`: size accepted + validated + present in payload.
- [ ] Verify fails; implement flag + status field + CSS scale + panel S/M/L toggle group (`aria-pressed`).
- [ ] e2e: each size changes host height; panel change persists across reload.
- [ ] Screenshots S/M/L.
- [ ] Commit: `feat: Add small/medium/large bar size presets`.

---

### Task 5 (PR 5) — feat: curated themes

**Files:** Modify `cmd/marquee/options.go`, `internal/status/status.go`, `internal/bar/bar.js` (theme CSS custom properties), `internal/bar/settings.js`, `internal/bar/prefs.js`; Test `options_test`, `status_test`, `e2e_test`.

**Interface:** `--theme default|<curated…>` (default `default`); status `theme`; bar sets `data-theme` selecting `--mq-bg/--mq-fg/--mq-border/--mq-chip-bg`. Branch chip untouched (stays hash-colored).

- [ ] Define the curated palette set; **each verified ≥4.5:1** for `--mq-fg` on `--mq-bg` and chip text on `--mq-chip-bg`, in both `prefers-color-scheme` modes (add a contrast unit check or document the computed ratios in the PR).
- [ ] `options_test`/`status_test`: theme accepted + validated + present; unknown theme errors.
- [ ] Implement flag + status + CSS var themes + panel `<select>`.
- [ ] e2e: theme change restyles chrome, branch chip unchanged, persists across reload.
- [ ] Screenshots of each theme, light + dark.
- [ ] Commit: `feat: Add curated, contrast-verified bar themes`.

---

### Task 6 (PR 6) — feat: pill show/hide + reorder (+ control affordance polish)

**Files:** Modify `cmd/marquee/options.go`, `internal/status/status.go`, `internal/bar/bar.js` (render pills from ordered list), `internal/bar/settings.js` (pill list UI), `internal/bar/prefs.js`; Test `options_test`, `status_test`, `e2e_test`.

**Interface:** `--pills` CSV over `branch,dirty,worktree,pr` (default all, that order); order = render order, omission = hidden; unknown name errors. Status `pills` = ordered string slice. Bar renders info pills by iterating this list, still honoring self-hide rules (worktree on main, pr absent, dirty clean).

- [ ] `options_test`: `--pills branch,pr` accepted (order preserved, others hidden); `--pills nope` errors; empty allowed (all pills hidden → bar shows only controls).
- [ ] `status_test`: `pills` ordered slice present.
- [ ] Implement flag + status field + ordered/visibility-aware render + panel pill rows (checkbox + labelled ↑/↓, disabled at ends).
- [ ] **Fold in the control-affordance fix:** switcher + ⚙ get a real border + glyph (`▾` / ⚙) so they read as controls, distinct from read-only chips.
- [ ] e2e: hide `pr`, reorder `worktree` above `branch`, reload → persists; keyboard reorder works; Reset restores default order/visibility.
- [ ] Screenshots (reordered, hidden pills, affordance before/after).
- [ ] Commit: `feat: Let the panel and --pills reorder and hide bar pills`.

---

## Self-review

- **Spec coverage:** position corners (T2), size (T4), theme (T5), pills order+visibility (T6), panel + persistence + Reset (T3), module split via improve-codebase-architecture (T3), two UX bug fixes (T1 layout shift, T6 affordance), a11y criteria (each task), tests + screenshots (each task). All spec sections mapped.
- **Type consistency:** `prefs.js` `DEFAULTS`/`load`/`merge`/`save`/`reset`/`validate` defined in T3 and consumed unchanged in T4–T6; status fields `position`/`size`/`theme`/`pills` named consistently across options/status/bar.
- **Ordering:** T1 and T2 are independent of the module split; T3 establishes prefs+panel; T4–T6 each extend all three layers (flag, status, panel) for one knob. Each PR rebases on the prior (all touch the bar subsystem), so they ship sequentially.
