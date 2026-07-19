# Switcher search design

**Date:** 2026-07-19
**Status:** Approved

## Problem

The worktree switcher menu renders every worktree as a flat list in git order. With
more than a handful of worktrees, finding the right one means scanning the whole
menu. Users should be able to type a few characters of a branch or slug and jump
straight to the worktree they mean.

## Scope

Frontend-only. All changes live in `internal/bar/bar.js` plus marker assertions in
`internal/bar/embed_test.go`. The status payload already carries each worktree's
slug and branch, so no Go code changes.

## UI

A text input pinned at the top of the existing `.menu` dropdown, always present
when the menu is open (the switcher already only renders with more than one
worktree). Opening the menu focuses the input instead of the first item; the
results list sits below it. Styling reuses the existing menu look and the bar's
standard focus ring. The query resets every time the menu closes, so each open
starts fresh with the full list.

## Matching and ranking

A pure, module-level scoring function in `bar.js`:

- Case-insensitive fuzzy subsequence match — every query character must appear in
  the candidate in order, but not necessarily adjacently.
- Each worktree is scored against both its branch and its slug; the better score
  wins. Non-matches drop out of the list.
- Scoring is small and deterministic: a bonus for consecutive matched characters,
  a bonus for matches at word boundaries (start of string, or after `/`, `-`,
  `_`), and earlier first-match position as the tiebreak.
- Empty query shows the full list in git order (main first), exactly as today.
  A non-empty query sorts results best-first.

## Interaction

- Typing filters and reorders live. The current worktree stays in the results
  when it matches, still labelled "(current)".
- **Enter** in the input activates the top result — identical semantics to
  clicking that item, reusing the existing switch flow (dirty-confirm, overlay,
  reload all unchanged).
- **ArrowDown** from the input moves focus into the first result; **ArrowUp**
  from the first result returns focus to the input (replacing the current
  "ArrowUp at top closes the menu" behaviour). **Escape** closes, as it does now.
- Zero matches renders a non-interactive "No matches" row; Enter then does
  nothing.

## Match highlighting

Matched characters are highlighted (bold) in the branch and slug lines, so fuzzy
hits like "ifm" matching `issue/fix-forms` are self-explanatory. The highlight is
built by composing per-character `<span>` elements assigned via `textContent`
only — the bar's never-`innerHTML` rule for dynamic text holds.

## Testing and verification

- New marker strings in the switcher block of `embed_test.go` (input class, the
  fuzzy-score function name, keyboard-handler markers, the no-matches string),
  each with the usual explanatory comment.
- `go test ./...` and `golangci-lint run` must pass.
- Live verification against a running dev server with a real browser, with
  screenshots of the filtered menu embedded in the PR.

## Out of scope

- Recency-based ordering of the unfiltered list (most-recently-switched first).
  Deliberately deferred; it would add persisted state via `prefs.js` and is a
  separate concern.
- Any backend ranking or a dedicated list endpoint.
