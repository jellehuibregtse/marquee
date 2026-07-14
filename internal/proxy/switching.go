package proxy

import (
	"html"
	"io"
	"net/http"
	"strings"
)

const switchingHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta http-equiv="refresh" content="1">
<title>switching…</title>
<style>
body { font-family: system-ui, sans-serif; display: grid; place-items: center; min-height: 100vh; margin: 0; }
p { color: #555; }
</style>
</head>
<body>
<main>
<h1>Switching to {{slug}}…</h1>
<p>marquee is restarting the dev server in another worktree. This page refreshes automatically.</p>
</main>
</body>
</html>
`

// serveSwitching answers a proxied request while a worktree switch is in
// progress: browser navigations get a self-refreshing page naming the target
// worktree, everything else a plain 503. Mirrors serveStarting — never a raw
// connection error while the child is mid-restart. slug comes from git's own
// worktree list, but is HTML-escaped defensively before it reaches the page.
func serveSwitching(w http.ResponseWriter, r *http.Request, slug string) {
	w.Header().Set("Cache-Control", "no-store")
	if acceptsHTML(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, renderSwitching(slug))
		return
	}
	http.Error(w, "marquee: switching worktree", http.StatusServiceUnavailable)
}

func renderSwitching(slug string) string {
	return strings.Replace(switchingHTML, "{{slug}}", html.EscapeString(slug), 1)
}
