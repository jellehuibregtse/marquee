// Package bar holds the embedded web assets for the injected bar.
package bar

import "embed"

// Assets contains the bar's JS and CSS, served by the proxy at /__marquee/*.
//
//go:embed bar.js bar.css
var Assets embed.FS
