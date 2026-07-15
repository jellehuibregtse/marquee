// Package bar holds the embedded web assets for the injected bar.
//
// The bar is a set of ES modules served same-origin: bar.js (the custom
// element) imports prefs.js (the pure prefs core) and settings.js (the panel
// UI). Each module carries its own CSS as a template literal injected into the
// shadow root, so there is one source of truth per module and no build step.
package bar

import "embed"

// Assets contains the bar's JS modules, served by the proxy under /__marquee/
// by file name (bar.js, prefs.js, settings.js).
//
//go:embed *.js
var Assets embed.FS
