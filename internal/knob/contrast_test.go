package knob

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
)

// TestThemePalettesMeetWCAGAA is the Go home of what used to be a comment in
// prefs.js: every theme, in every scheme it defines, must keep --mq-fg legible
// on --mq-bg and chip text legible on the chip background. The catalog now owns
// the palettes, so the guarantee is computed over that data rather than trusted
// by eye. The chip background can be translucent (the default theme's rgba), so
// it is composited over the bar background before measuring, exactly as the
// browser paints it. The branch chip is deliberately excluded — it keeps its own
// hash-contrast guarantee and no theme sets its color.
func TestThemePalettesMeetWCAGAA(t *testing.T) {
	const minRatio = 4.5
	for _, theme := range Default.Themes.Choices {
		schemes := map[string]Palette{"light": theme.Light}
		if theme.Dark != nil {
			schemes["dark"] = *theme.Dark
		}
		for scheme, p := range schemes {
			bg := mustColor(t, theme.ID, scheme, "bg", p.Bg)
			fg := mustColor(t, theme.ID, scheme, "fg", p.Fg)
			chip := mustColor(t, theme.ID, scheme, "chipBg", p.ChipBg).over(bg)

			if r := contrast(fg, bg); r < minRatio {
				t.Errorf("theme %q (%s): --mq-fg on --mq-bg = %.2f:1, want >= %.1f:1", theme.ID, scheme, r, minRatio)
			}
			if r := contrast(fg, chip); r < minRatio {
				t.Errorf("theme %q (%s): chip text on --mq-chip-bg = %.2f:1, want >= %.1f:1", theme.ID, scheme, r, minRatio)
			}
		}
	}
}

// color is a straight-alpha sRGB color with channels in [0,1].
type color struct{ r, g, b, a float64 }

// over composites c (which may be translucent) onto an opaque background,
// yielding the opaque color the browser actually paints.
func (c color) over(bg color) color {
	mix := func(top, bottom float64) float64 { return top*c.a + bottom*(1-c.a) }
	return color{mix(c.r, bg.r), mix(c.g, bg.g), mix(c.b, bg.b), 1}
}

func mustColor(t *testing.T, theme, scheme, field, value string) color {
	t.Helper()
	c, err := parseColor(value)
	if err != nil {
		t.Fatalf("theme %q (%s) %s = %q: %v", theme, scheme, field, value, err)
	}
	return c
}

func parseColor(value string) (color, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "#") {
		return parseHex(value)
	}
	if strings.HasPrefix(value, "rgb") {
		return parseRGB(value)
	}
	return color{}, fmt.Errorf("unrecognized color syntax")
}

func parseHex(value string) (color, error) {
	hex := strings.TrimPrefix(value, "#")
	if len(hex) != 6 {
		return color{}, fmt.Errorf("expected #rrggbb")
	}
	ch := func(s string) (float64, error) {
		n, err := strconv.ParseInt(s, 16, 0)
		return float64(n) / 255, err
	}
	r, err := ch(hex[0:2])
	if err != nil {
		return color{}, err
	}
	g, err := ch(hex[2:4])
	if err != nil {
		return color{}, err
	}
	b, err := ch(hex[4:6])
	if err != nil {
		return color{}, err
	}
	return color{r, g, b, 1}, nil
}

func parseRGB(value string) (color, error) {
	open := strings.IndexByte(value, '(')
	if open < 0 || !strings.HasSuffix(value, ")") {
		return color{}, fmt.Errorf("expected rgb(...) or rgba(...)")
	}
	parts := strings.Split(value[open+1:len(value)-1], ",")
	if len(parts) != 3 && len(parts) != 4 {
		return color{}, fmt.Errorf("expected 3 or 4 components")
	}
	comp := func(s string, scale float64) (float64, error) {
		n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		return n / scale, err
	}
	r, err := comp(parts[0], 255)
	if err != nil {
		return color{}, err
	}
	g, err := comp(parts[1], 255)
	if err != nil {
		return color{}, err
	}
	b, err := comp(parts[2], 255)
	if err != nil {
		return color{}, err
	}
	a := 1.0
	if len(parts) == 4 {
		if a, err = comp(parts[3], 1); err != nil {
			return color{}, err
		}
	}
	return color{r, g, b, a}, nil
}

// relativeLuminance is the WCAG sRGB relative luminance of an opaque color.
func relativeLuminance(c color) float64 {
	lin := func(v float64) float64 {
		if v <= 0.03928 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c.r) + 0.7152*lin(c.g) + 0.0722*lin(c.b)
}

// contrast is the WCAG contrast ratio between two opaque colors.
func contrast(a, b color) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}
