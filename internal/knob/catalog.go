// Package knob is the single owner of the bar's knobs — position, size, theme,
// and pills. It holds each knob's ids, default, labels, and (for themes) the
// contrast-verified palettes, so the value sets are no longer a distributed
// enum spread across Go flag validation, prefs.js, settings.js, and bar.js CSS.
//
// Both sides read from this one table: the CLI flag parsers look up validity
// here, and Default rides the /__marquee/status payload so the bar's JS derives
// its value lists, labels, and theme CSS from the same source it validated
// against. That closes the drift the split enum allowed — a theme the validator
// accepted but the CSS had no palette for. Adding a knob value is a one-line
// edit here.
package knob

import "strings"

// Palette is the bar's four themable custom properties (--mq-bg, --mq-fg,
// --mq-border, --mq-chip-bg). Each value is verbatim CSS: a hex color or, for
// the default theme's translucent chip, an rgba() string. bar.js emits these
// into a generated <style>, so a theme is a value set rather than a hardcoded
// CSS block.
type Palette struct {
	Bg     string `json:"bg"`
	Fg     string `json:"fg"`
	Border string `json:"border"`
	ChipBg string `json:"chipBg"`
}

// Theme pairs a theme's id and label with its palette(s). Light is the
// light-scheme palette; Dark, when set, is the dark-scheme override that makes
// the theme scheme-aware. Only the default theme carries a Dark palette — the
// curated themes are fixed and look the same in both schemes.
type Theme struct {
	ID    string   `json:"id"`
	Label string   `json:"label"`
	Light Palette  `json:"light"`
	Dark  *Palette `json:"dark,omitempty"`
}

// Choice is an id+label option for the value-only knobs (position, size, pill).
type Choice struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Knob is a value-only knob: an ordered choice list plus the default value. For
// position, size, and theme the default is a single id; for pills it is the CSV
// of the full ordered list (an omitted id hides that pill).
type Knob struct {
	Default string   `json:"default"`
	Choices []Choice `json:"choices"`
}

// Valid reports whether id is one of the knob's choices.
func (k Knob) Valid(id string) bool {
	for _, c := range k.Choices {
		if c.ID == id {
			return true
		}
	}
	return false
}

// IDs returns the choice ids in order.
func (k Knob) IDs() []string {
	ids := make([]string, len(k.Choices))
	for i, c := range k.Choices {
		ids[i] = c.ID
	}
	return ids
}

// List renders the choice ids for an error message: "a, b, c".
func (k Knob) List() string { return strings.Join(k.IDs(), ", ") }

// Themes is the theme knob: an ordered theme list (each carrying its palettes)
// plus the default theme id.
type Themes struct {
	Default string  `json:"default"`
	Choices []Theme `json:"choices"`
}

// Valid reports whether id is one of the theme ids.
func (t Themes) Valid(id string) bool {
	for _, c := range t.Choices {
		if c.ID == id {
			return true
		}
	}
	return false
}

// IDs returns the theme ids in order.
func (t Themes) IDs() []string {
	ids := make([]string, len(t.Choices))
	for i, c := range t.Choices {
		ids[i] = c.ID
	}
	return ids
}

// List renders the theme ids for an error message: "a, b, c".
func (t Themes) List() string { return strings.Join(t.IDs(), ", ") }

// Catalog is the whole knob set. Default rides the status payload as its
// "catalog" field, so the bar's JS derives everything below from the same table
// the CLI validates against.
type Catalog struct {
	Positions Knob   `json:"positions"`
	Sizes     Knob   `json:"sizes"`
	Themes    Themes `json:"themes"`
	Pills     Knob   `json:"pills"`
}

// Default is the embedded knob catalog: the single source the CLI flags and the
// bar both read. The theme palettes are the contrast-verified values (the
// package's contrast test guarantees --mq-fg on --mq-bg and chip text on the
// composited --mq-chip-bg clear WCAG AA) lifted verbatim from the bar's original
// CSS, so the seam move stays pixel-identical.
var Default = Catalog{
	Positions: Knob{
		Default: "bottom-left",
		Choices: []Choice{
			{ID: "bottom-left", Label: "Bottom left"},
			{ID: "bottom-right", Label: "Bottom right"},
			{ID: "top-left", Label: "Top left"},
			{ID: "top-right", Label: "Top right"},
		},
	},
	Sizes: Knob{
		Default: "medium",
		Choices: []Choice{
			{ID: "small", Label: "Small"},
			{ID: "medium", Label: "Medium"},
			{ID: "large", Label: "Large"},
		},
	},
	Themes: Themes{
		Default: "default",
		Choices: []Theme{
			{
				ID:    "default",
				Label: "Default",
				Light: Palette{Bg: "#f5f5f4", Fg: "#1c1c1c", Border: "#d0d0ce", ChipBg: "rgba(0, 0, 0, 0.07)"},
				Dark:  &Palette{Bg: "#262626", Fg: "#ededed", Border: "#4d4d4d", ChipBg: "rgba(255, 255, 255, 0.12)"},
			},
			{ID: "midnight", Label: "Midnight", Light: Palette{Bg: "#0f172a", Fg: "#e2e8f0", Border: "#334155", ChipBg: "#1e293b"}},
			{ID: "sand", Label: "Sand", Light: Palette{Bg: "#f4ecd8", Fg: "#3a2e1a", Border: "#d9cdb3", ChipBg: "#e6d9ba"}},
			{ID: "forest", Label: "Forest", Light: Palette{Bg: "#0f1f17", Fg: "#d6e8dc", Border: "#2d4a3a", ChipBg: "#1a3226"}},
		},
	},
	Pills: Knob{
		Default: "branch,dirty,worktree,pr",
		Choices: []Choice{
			{ID: "branch", Label: "Branch"},
			{ID: "dirty", Label: "Uncommitted changes"},
			{ID: "worktree", Label: "Worktree"},
			{ID: "pr", Label: "Pull request"},
		},
	},
}
