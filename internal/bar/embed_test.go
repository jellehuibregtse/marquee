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
		// imports the pure prefs core from the sibling prefs.js module, served
		// same-origin under the 'self' CSP relaxation.
		`from "./prefs.js"`,
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
