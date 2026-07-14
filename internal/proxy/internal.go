package proxy

import (
	"net"
	"net/http"
	"strings"
)

// InternalMux serves the /__marquee/ namespace. Every request passes the
// same guards before any handler runs: the Host header (port stripped)
// must be on the local allowlist — defeating DNS rebinding, where a
// hostile page resolves its own domain to 127.0.0.1 — and every response
// carries Cache-Control: no-store. Handlers registered through Handle
// cannot bypass the guards; there is no other way in.
type InternalMux struct {
	mux         *http.ServeMux
	allowHosts  map[string]bool
	allowSuffix []string
}

// NewInternalMux builds a guarded mux. The built-in allowlist is
// localhost, 127.0.0.1, ::1 and *.localhost (RFC 6761 reserves .localhost
// for loopback, so it is safe to trust by default). extraHosts are
// operator-supplied entries, compared case-insensitively with the port
// ignored: a bare host is an exact match, while a wildcard of the form
// "*.example.test" (or ".example.test") registers a suffix match anchored
// on a dot boundary, so it matches sub.example.test but neither
// example.test itself nor evil-example.test.
func NewInternalMux(extraHosts ...string) *InternalMux {
	allow := map[string]bool{
		"localhost": true,
		"127.0.0.1": true,
		"::1":       true,
	}
	var suffixes []string
	for _, h := range extraHosts {
		if suffix, ok := hostSuffix(h); ok {
			suffixes = append(suffixes, suffix)
			continue
		}
		allow[strings.ToLower(hostWithoutPort(h))] = true
	}
	return &InternalMux{mux: http.NewServeMux(), allowHosts: allow, allowSuffix: suffixes}
}

// hostSuffix recognises the wildcard forms "*.example.test" and
// ".example.test" and returns the dot-anchored suffix (".example.test",
// lowercased) to match against. A plain host returns ok=false.
func hostSuffix(entry string) (string, bool) {
	entry = strings.ToLower(strings.TrimSpace(entry))
	switch {
	case strings.HasPrefix(entry, "*."):
		entry = entry[1:]
	case !strings.HasPrefix(entry, "."):
		return "", false
	}
	if entry == "." || strings.Contains(entry, "*") {
		return "", false
	}
	return entry, true
}

// Handle registers handler for pattern behind the guards.
func (m *InternalMux) Handle(pattern string, handler http.Handler) {
	m.mux.Handle(pattern, handler)
}

// HandleFunc registers handler for pattern behind the guards.
func (m *InternalMux) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	m.mux.HandleFunc(pattern, handler)
}

func (m *InternalMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !m.hostAllowed(r.Host) {
		http.Error(w, "marquee: forbidden host", http.StatusForbidden)
		return
	}
	m.mux.ServeHTTP(w, r)
}

func (m *InternalMux) hostAllowed(hostport string) bool {
	host := strings.ToLower(hostWithoutPort(hostport))
	if host == "" {
		return false
	}
	if m.allowHosts[host] {
		return true
	}
	if hasLabelSuffix(host, ".localhost") {
		return true
	}
	for _, suffix := range m.allowSuffix {
		if hasLabelSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// hasLabelSuffix reports whether host ends in suffix with at least one
// label before it, so a bare ".lvh.me" or ".localhost" Host (no label)
// does not match a "*.lvh.me"/"*.localhost" wildcard.
func hasLabelSuffix(host, suffix string) bool {
	return len(host) > len(suffix) && strings.HasSuffix(host, suffix)
}

func hostWithoutPort(hostport string) string {
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		return host
	}
	return strings.Trim(hostport, "[]")
}
