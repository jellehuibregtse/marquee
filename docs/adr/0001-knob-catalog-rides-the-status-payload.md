# Knob catalog rides the status payload

The bar's knobs (position, size, theme, pills) were a distributed enum: value sets, defaults, labels, and theme palettes maintained independently in Go flag validation, `prefs.js`, `settings.js`, `bar.js` CSS, and the status documentation — adding a theme meant editing at least four files, and the Go validator could accept a theme name the CSS had no palette for. We gave the knobs a single owner, one embedded Go catalog, and chose the existing `/__marquee/status` payload as its transport to the browser: the bar never renders before its first status fetch, so the catalog is always there when needed, at zero extra requests.

## Considered options

- **Build-time codegen** (`go generate` emitting the JS tables from the Go catalog): rejected — it adds build machinery to a deliberately stdlib-only project and catches drift only when generation runs.
- **Parity tests** (keep every copy, assert they agree): rejected — it polices the distributed enum instead of giving it an owner; the multi-file edit and two-language duplication remain.

## Consequences

`bar.js` builds each theme's CSS custom-property set from payload data instead of hardcoded palettes; the WCAG ≥4.5:1 contrast guarantee moves from a comment to a Go unit test over the palette data; the JS keeps built-in defaults only as the fail-open fallback for a missing or stale payload.
