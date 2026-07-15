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
		// Curated themes (PR 5): the chrome palette lives in custom properties so
		// a theme is a value set, the effective theme rides a data-theme
		// attribute (fail-open to default), and each curated theme is a
		// scheme-independent :host attribute rule. The branch chip is untouched —
		// no theme selector sets its color, preserving its hash-contrast guarantee.
		"status.theme",
		"data-theme",
		"--mq-bg",
		"--mq-fg",
		"--mq-border",
		"--mq-chip-bg",
		`background: var(--mq-bg)`,
		`background: var(--mq-chip-bg)`,
		`:host([data-theme="midnight"])`,
		`:host([data-theme="sand"])`,
		`:host([data-theme="forest"])`,
		"safeHttpUrl",
		`url.protocol === "https:"`,
		// Worktree switcher (M4-T2): the POST target, the token header echoed
		// from the injected attribute, the accessible dropdown, and the
		// dirty-refusal confirm path.
		"/__marquee/switch",
		"X-Marquee-Token",
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
		`from "./prefs.js"`,
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
