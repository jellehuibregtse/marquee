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
	mux        *http.ServeMux
	allowHosts map[string]bool
}

// NewInternalMux builds a guarded mux. The built-in allowlist is
// localhost, 127.0.0.1, ::1, *.localhost and *.lvh.me; extraHosts are
// additional exact matches (compared case-insensitively, port ignored).
func NewInternalMux(extraHosts ...string) *InternalMux {
	allow := map[string]bool{
		"localhost": true,
		"127.0.0.1": true,
		"::1":       true,
	}
	for _, h := range extraHosts {
		allow[strings.ToLower(hostWithoutPort(h))] = true
	}
	return &InternalMux{mux: http.NewServeMux(), allowHosts: allow}
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
	return strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".lvh.me")
}

func hostWithoutPort(hostport string) string {
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		return host
	}
	return strings.Trim(hostport, "[]")
}
