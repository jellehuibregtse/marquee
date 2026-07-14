package proxy

import (
	"io"
	"net/http"
	"strings"
)

const startingHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta http-equiv="refresh" content="1">
<title>starting…</title>
<style>
body { font-family: system-ui, sans-serif; display: grid; place-items: center; min-height: 100vh; margin: 0; }
p { color: #555; }
</style>
</head>
<body>
<main>
<h1>App is starting…</h1>
<p>marquee is waiting for the dev server to accept connections. This page refreshes automatically.</p>
</main>
</body>
</html>
`

// serveStarting answers a request while the upstream is unreachable:
// browser navigations get a self-refreshing HTML page, everything else a
// plain 503. This is the fail-open shape for upstream trouble — never a
// raw connection error or stack trace.
func serveStarting(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if acceptsHTML(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, startingHTML)
		return
	}
	http.Error(w, "marquee: app is starting", http.StatusServiceUnavailable)
}

func acceptsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}
