package bar

import (
	"strings"
	"testing"
)

func TestBarScriptEmbedded(t *testing.T) {
	data, err := Assets.ReadFile("bar.js")
	if err != nil {
		t.Fatalf("read embedded bar.js: %v", err)
	}
	js := string(data)
	if len(js) == 0 {
		t.Fatal("embedded bar.js is empty")
	}

	for _, marker := range []string{
		"customElements.define",
		"marquee-bar",
		`role="status"`,
		"/__marquee/status",
		`rel="noreferrer"`,
		// Four-corner positioning (PR 2): the bar carries the corner on a
		// data-position attribute and CSS anchors each corner; the worktree
		// menu flips its horizontal/vertical anchor so it never clips.
		"status.position",
		"data-position",
		`:host([data-position="bottom-left"])`,
		`:host([data-position="bottom-right"])`,
		`:host([data-position="top-left"])`,
		`:host([data-position="top-right"])`,
		`:host([data-position$="-right"]) .menu`,
		`:host([data-position^="top-"]) .menu`,
		// Size presets (PR 4): the bar carries the preset on a data-size
		// attribute and every dimension multiplies through the single --mq-scale
		// custom property, so one attribute rescales the whole bar.
		"status.size",
		"data-size",
		"--mq-scale",
		`:host([data-size="small"])`,
		`:host([data-size="large"])`,
		"calc(28px * var(--mq-scale, 1))",
		// Themes via the knob catalog (knob-catalog refactor): the chrome palette
		// lives in custom properties, the effective theme rides a data-theme
		// attribute (fail-open to default), and each theme's palette comes from the
		// catalog in the status payload — #applyThemeStyles builds a per-theme
		// :host([data-theme=…]) rule from that data into a dedicated <style>, so a
		// theme is a value set, not a hardcoded CSS block. The static :host palette
		// remains only as the pre-payload fallback. The branch chip is untouched —
		// no theme selector sets its color, preserving its hash-contrast guarantee.
		"status.theme",
		"status.catalog",
		"data-theme",
		"--mq-bg",
		"--mq-fg",
		"--mq-border",
		"--mq-chip-bg",
		`background: var(--mq-bg)`,
		`background: var(--mq-chip-bg)`,
		"mq-themes",
		"#applyThemeStyles",
		"themeRule",
		"paletteDecls",
		// Palette guard: every palette field is validated before any rule is
		// emitted, so a malformed payload can neither inject "undefined" custom
		// property values over the static fallback nor break out of the generated
		// <style> — a bad palette yields no rule and the fallback wins (fail-open).
		"cssValue",
		"validPalette",
		`:host([data-theme="`,
		"effectiveCatalog",
		"makeValidators",
		"safeHttpUrl",
		`url.protocol === "https:"`,
		// Worktree switcher (M4-T2): the POST target, the token header echoed
		// from the injected attribute, the accessible dropdown, and the
		// dirty-refusal confirm path.
		"/__marquee/switch",
		"X-Marquee-Token",
		// A successful switch reloads the whole page: the endpoint only returns
		// once the target server is healthy, so the reload lands on the new
		// worktree's content rather than leaving the old page on screen.
		"location.reload()",
		// Switch loading overlay: the moment a switch starts a viewport-filling
		// scrim goes up inside the shadow DOM, and the reload is deferred until a
		// fast status poll reports the target worktree running, so the reload hits
		// a warm server. Any non-reload path tears the overlay down (fail-open).
		`class="overlay"`,
		"overlay-spinner",
		"#showOverlay",
		"#hideOverlay",
		"#waitForWorktreeReady",
		// Overlay polish: the scrim sits below the bar's own stacking level so the
		// bar stays visible and legible above the dimmed page during a switch, and
		// the ready→reload beat swaps the spinner for a settled check instead of
		// leaving a spinner frozen mid-navigation.
		"overlay-check",
		"#markOverlayReady",
		"z-index: 2",
		"z-index: 1",
		"SWITCH_POLL_INTERVAL_MS",
		`status.child.state === "running"`,
		"status.worktree.slug === slug",
		// A failed switch surfaces its state in the bar — not only the console —
		// via #switchError and the .switch-error accent, so a rejected or failed
		// switch is visible and the control stays operable for a retry.
		"#switchError",
		"switch-error",
		`getAttribute("token")`,
		`aria-haspopup="menu"`,
		`role="menu"`,
		"aria-expanded",
		"textContent",
		// Switch menu rows distinguish the branch from the worktree slug, so
		// the primary label reads the branch and the secondary reads the slug.
		"worktree.branch",
		"item-branch",
		"item-slug",
		// PR-chip layout shift (PR 1): the slot reserves a fixed width and
		// renders a reduced-motion-aware shimmer skeleton while the PR is still
		// unknown, so the switcher/toggle don't shift when the async poll
		// resolves. These markers pin that behavior into the asset.
		"pr-text",
		"skeleton",
		"marquee-shimmer",
		"prefers-reduced-motion",
		"#statusPolls",
		// ES-module split (PR 3): bar.js is no longer self-contained — it
		// imports the pure prefs core and the settings panel from sibling
		// modules, served same-origin under the 'self' CSP relaxation.
		`from "./prefs.js"`,
		`from "./settings.js"`,
		// Settings panel (PR 3): the ⚙ button is a real control with an
		// accessible name, and bar.js persists panel changes to localStorage.
		`class="gear"`,
		`aria-label="Bar settings"`,
		"createSettingsPanel",
		"localStore",
		// Pill show/hide + reorder (PR 6): the bar reads the ordered pill list
		// from status, iterates the shared PILL_IDS table, gates each pill on
		// membership, and lays the elements out in order before the controls.
		"status.pills",
		"PILL_IDS",
		"#orderPills",
		"insertBefore",
		// Control-affordance polish (PR 6): the worktree switch gains a solid
		// border and a ▾ caret so it reads as an operable control, not a chip.
		"switch-caret",
		"▾",
		"border: 1px solid var(--mq-border)",
		// Dark-mode icon contrast: the bar's symbol glyphs carry the U+FE0E
		// text-presentation selector so a platform that would default them to
		// emoji can't render them as fixed-color glyphs that ignore --mq-fg. They
		// stay monochrome and follow the themed foreground in every scheme.
		"⚙︎",
		"▾︎",
		"●︎",
	} {
		if !strings.Contains(js, marker) {
			t.Errorf("bar.js missing expected marker %q", marker)
		}
	}

	for _, forbidden := range []string{
		"eval(",
		"http://",
		"https://",
		"import(",
	} {
		if strings.Contains(js, forbidden) {
			t.Errorf("bar.js contains forbidden pattern %q", forbidden)
		}
	}
}

func TestPrefsModuleEmbedded(t *testing.T) {
	data, err := Assets.ReadFile("prefs.js")
	if err != nil {
		t.Fatalf("read embedded prefs.js: %v", err)
	}
	js := string(data)
	if len(js) == 0 {
		t.Fatal("embedded prefs.js is empty")
	}

	// The pure prefs core (PR 3): the exported interface consumed by bar.js and
	// settings.js, and the four-corner position table it validates against.
	for _, marker := range []string{
		"export const DEFAULTS",
		"export const POSITIONS",
		"export function load",
		"export function merge",
		"export function save",
		"export function reset",
		"export function validate",
		"marquee-bar-prefs",
		"bottom-left",
		"top-right",
		// Size presets (PR 4): the size table and its default, validated the
		// same generic way as position.
		"export const SIZES",
		`size: "medium"`,
		// Curated themes (PR 5): the theme table and its default, validated the
		// same generic way as position and size.
		"export const THEMES",
		`theme: "default"`,
		"midnight",
		"forest",
		// Pill show/hide + reorder (PR 6): the shared pill table, the pills
		// default (all four in order), and the array validator that drops an
		// invalid stored value so the default wins.
		"export const PILL_IDS",
		`pills: ["branch", "dirty", "worktree", "pr"]`,
		"Array.isArray",
		// Knob catalog (knob-catalog refactor): the built-in fallback catalog with
		// ids and labels, plus the payload-driven derivation — effectiveCatalog
		// reads the status catalog (fallback per knob) and makeValidators builds
		// the validators from it, so value lists flow from the payload while the
		// built-ins remain only as the fail-open fallback.
		"export const FALLBACK_CATALOG",
		"export function effectiveCatalog",
		"export function makeValidators",
	} {
		if !strings.Contains(js, marker) {
			t.Errorf("prefs.js missing expected marker %q", marker)
		}
	}

	// prefs.js is pure: no DOM, no network, no dynamic code.
	for _, forbidden := range []string{
		"eval(",
		"document",
		"localStorage",
		"fetch(",
		"import(",
	} {
		if strings.Contains(js, forbidden) {
			t.Errorf("prefs.js contains forbidden pattern %q", forbidden)
		}
	}
}

func TestSettingsModuleEmbedded(t *testing.T) {
	data, err := Assets.ReadFile("settings.js")
	if err != nil {
		t.Fatalf("read embedded settings.js: %v", err)
	}
	js := string(data)
	if len(js) == 0 {
		t.Fatal("embedded settings.js is empty")
	}

	// The ⚙ popover (PR 3): the exported interface bar.js consumes, the
	// accessible Position radiogroup, the Reset control, and the disclosure
	// wiring (Escape/outside-pointer close, focus return to the gear).
	for _, marker := range []string{
		"export const PANEL_CSS",
		"export function createSettingsPanel",
		// Knob catalog (knob-catalog refactor): the panel derives every value list
		// and label from the live catalog bar.js hands it via getCatalog, sourced
		// from the status payload, so settings.js no longer hardcodes the value
		// sets or imports them from prefs.js.
		"getCatalog",
		"radiogroup",
		`type = "radio"`,
		"marquee-position",
		// Size control (PR 4): an S/M/L toggle-button group with aria-pressed,
		// wired to the onSize callback and kept in sync by sync().
		"settings-size",
		"aria-pressed",
		"onSize",
		// Theme control (PR 5): a native, labelled <select> listing the curated
		// themes, wired to the onTheme callback and kept in sync by sync().
		"settings-theme",
		`createElement("select")`,
		"onTheme",
		"Reset",
		"onOutsidePointer",
		`"Escape"`,
		"aria-expanded",
		".settings-menu",
		// The panel entrance animation is disabled under reduced motion.
		"prefers-reduced-motion",
		// Pills section (PR 6): a per-id list of checkbox rows (shown/hidden)
		// with ↑/↓ reorder buttons, wired to the onPills callback and rebuilt by
		// syncPills only when the effective order or visibility changed.
		"onPills",
		"settings-pill",
		`type = "checkbox"`,
		"↑",
		"↓",
		// Dark-mode icon contrast: the reorder arrows carry the U+FE0E
		// text-presentation selector so they stay monochrome and follow the panel
		// foreground rather than rendering as fixed-color emoji.
		"↑︎",
		"↓︎",
		"syncPills",
	} {
		if !strings.Contains(js, marker) {
			t.Errorf("settings.js missing expected marker %q", marker)
		}
	}

	for _, forbidden := range []string{
		"eval(",
		"http://",
		"https://",
		"import(",
		"fetch(",
	} {
		if strings.Contains(js, forbidden) {
			t.Errorf("settings.js contains forbidden pattern %q", forbidden)
		}
	}
}
