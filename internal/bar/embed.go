// Package bar holds the embedded web assets for the injected bar.
//
// The bar is a single self-contained ES module: bar.js carries its own CSS
// as a template literal injected into the shadow root, so there is exactly
// one source of truth and no build step.
package bar

import "embed"

// Assets contains the bar's JS, served by the proxy at /__marquee/bar.js.
//
//go:embed bar.js
var Assets embed.FS
