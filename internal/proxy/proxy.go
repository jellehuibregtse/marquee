// Package proxy implements the reverse proxy in front of the child dev
// server: Host-preserving passthrough, the /__marquee/ internal carve-out
// behind a guarded mux, and a self-refreshing "starting" page while the
// upstream is not accepting connections.
package proxy

import (
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jellehuibregtse/marquee/internal/switching"
)

const internalPrefix = "/__marquee/"

// Config configures a Handler. InternalPort is the loopback port the
// upstream (child) server listens on.
type Config struct {
	InternalPort int
	// UpstreamURL, when non-nil, is the explicit upstream marquee proxies
	// to (attach mode, where marquee manages no child). It overrides
	// InternalPort for both the reverse-proxy target and the liveness
	// probe. When nil, the upstream falls back to 127.0.0.1:InternalPort
	// (wrapper mode).
	UpstreamURL *url.URL
	// RelaxCSP, when true, makes marquee rewrite the Content-Security-Policy
	// of responses it injects the bar into so the bar's same-origin
	// resources are permitted: 'self' is ensured in the script-governing
	// directive (for /__marquee/bar.js) and in connect-src (for the
	// /__marquee/status fetch). Nothing else is changed, report-only CSP is
	// left alone, and only successfully-injected responses are touched. See
	// relaxCSPForBar and docs/security.md.
	RelaxCSP bool
	// AllowHosts are extra Host values accepted on /__marquee/* in
	// addition to the built-in loopback allowlist. A plain entry is an
	// exact match (port ignored); a "*.example.test" wildcard matches any
	// subdomain. See NewInternalMux for the matching rules.
	AllowHosts []string
	// SwitchToken is the per-process CSRF token for the worktree switcher. It
	// is rendered into the injected <marquee-bar> element so bar.js can echo
	// it as the X-Marquee-Token header on POST /__marquee/switch, and the
	// switch handler compares it in constant time. Empty means no switcher
	// (attach mode, or token minting failed): the token-less snippet is used.
	SwitchToken string
	// Logger receives operational messages (upstream errors). Defaults
	// to log.Default().
	Logger *log.Logger
	// ProbeTimeout bounds a single upstream liveness dial. Defaults to
	// 250ms.
	ProbeTimeout time.Duration
	// ProbeTTL is how long a liveness result is cached. Defaults to
	// 500ms.
	ProbeTTL time.Duration
}

// Handler is the http.Handler marquee serves on its listen address. It
// proxies everything to 127.0.0.1:<InternalPort> except the /__marquee/
// namespace, which is handled by the guarded internal mux.
type Handler struct {
	reverse  *httputil.ReverseProxy
	internal *InternalMux
	probe    *probe
	logger   *log.Logger

	// switchSrc holds the in-flight switch's Progress source, so proxied
	// navigations can be shown a "switching…" page instead of racing a
	// restarting child. It is a narrow typed seam over the leaf switching
	// package, not the switcher itself: the orchestrator implements SwitchSource
	// and main wires it via SetSwitchSource, so the proxy learns of switches
	// without importing (and cycling with) the switcher. Stored in an
	// atomic.Value so reads stay race-free.
	switchSrc atomic.Value
}

// SwitchSource reports the progress of an in-flight worktree switch. The switch
// orchestrator implements it; the proxy consults only Progress().Slug to decide
// whether a navigation should see the interstitial.
type SwitchSource interface {
	Progress() switching.Progress
}

// switchSourceHolder gives atomic.Value a single concrete type to store even
// though SwitchSource is an interface.
type switchSourceHolder struct{ src SwitchSource }

// New builds a Handler for the given configuration.
func New(cfg Config) *Handler {
	logger := cfg.Logger
	if logger == nil {
		logger = log.Default()
	}
	target := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.InternalPort))}
	probeAddr := target.Host
	if cfg.UpstreamURL != nil {
		target = cfg.UpstreamURL
		probeAddr = upstreamProbeAddr(cfg.UpstreamURL)
	}
	// Read once at startup; injection decisions never consult the
	// environment at request time.
	switches := newBarSwitches(os.Getenv("MARQUEE_DISABLE_BAR") == "1")

	h := &Handler{
		internal: NewInternalMux(cfg.AllowHosts...),
		probe: &probe{
			addr:    probeAddr,
			timeout: defaultDuration(cfg.ProbeTimeout, 250*time.Millisecond),
			ttl:     defaultDuration(cfg.ProbeTTL, 500*time.Millisecond),
		},
		logger: logger,
	}
	h.internal.HandleFunc("GET /__marquee/toggle", switches.handleToggle)
	h.reverse = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = r.In.Host
			r.Out.Header.Set("Accept-Encoding", "identity")
			for _, value := range r.In.Header.Values("X-Marquee") {
				if strings.EqualFold(value, "skip") {
					r.Out = markBarSkip(r.Out)
					break
				}
			}
			// The app never sees marquee plumbing.
			r.Out.Header.Del("X-Marquee")
		},
		FlushInterval:  -1,
		ModifyResponse: newInjector(logger, switches, cfg.RelaxCSP, cfg.SwitchToken).modifyResponse,
		ErrorLog:       logger,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Printf("marquee: upstream error for %s %s: %v", r.Method, r.URL.Path, err)
			h.probe.markDown()
			serveStarting(w, r)
		},
	}
	return h
}

// Internal returns the guarded mux for the /__marquee/ namespace. Later
// features (status endpoint, bar assets) register their handlers here so
// they inherit the Host allowlist and no-store guards by construction.
func (h *Handler) Internal() *InternalMux {
	return h.internal
}

// SetSwitchSource installs the source reporting an in-progress worktree
// switch's progress. While it reports a non-empty slug, proxied (non-internal)
// navigations receive a self-refreshing "switching…" page rather than being
// forwarded to a child that is mid-restart. main wires this after construction;
// before it does, the proxy behaves as before.
func (h *Handler) SetSwitchSource(src SwitchSource) {
	h.switchSrc.Store(switchSourceHolder{src: src})
}

func (h *Handler) switchingSlug() string {
	holder, _ := h.switchSrc.Load().(switchSourceHolder)
	if holder.src == nil {
		return ""
	}
	return holder.src.Progress().Slug
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if isInternalPath(r.URL.Path) {
		h.internal.ServeHTTP(w, r)
		return
	}
	if slug := h.switchingSlug(); slug != "" {
		serveSwitching(w, r, slug)
		return
	}
	if !h.probe.up() {
		serveStarting(w, r)
		return
	}
	h.reverse.ServeHTTP(sniffGuard{w}, r)
}

// sniffGuard keeps the proxy transparent when the upstream sent no
// Content-Type: net/http sniffs one from the body prefix if the header is
// still unset when headers hit the wire, and with FlushInterval -1 whether
// that happens races the initial flush timer against the first body write.
// Setting the key to an explicit nil is net/http's documented way to
// suppress sniffing without emitting a header. ResponseController reaches
// Flush and Hijack on the wrapped writer through Unwrap, so streaming and
// protocol upgrades are unaffected.
type sniffGuard struct {
	http.ResponseWriter
}

func (w sniffGuard) WriteHeader(status int) {
	if _, ok := w.Header()["Content-Type"]; !ok {
		w.Header()["Content-Type"] = nil
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w sniffGuard) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func isInternalPath(path string) bool {
	return path == internalPrefix[:len(internalPrefix)-1] || strings.HasPrefix(path, internalPrefix)
}

// upstreamProbeAddr turns an upstream URL into a host:port the liveness
// probe can dial, filling in the scheme's default port when the URL omits
// one so net.DialTimeout always has a port.
func upstreamProbeAddr(u *url.URL) string {
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return net.JoinHostPort(u.Hostname(), port)
}

func defaultDuration(d, fallback time.Duration) time.Duration {
	if d <= 0 {
		return fallback
	}
	return d
}

// probe caches a TCP liveness check of the upstream so every request does
// not pay a dial.
type probe struct {
	addr    string
	timeout time.Duration
	ttl     time.Duration

	mu        sync.Mutex
	lastCheck time.Time
	lastUp    bool
}

func (p *probe) up() bool {
	p.mu.Lock()
	if !p.lastCheck.IsZero() && time.Since(p.lastCheck) < p.ttl {
		defer p.mu.Unlock()
		return p.lastUp
	}
	p.mu.Unlock()

	conn, err := net.DialTimeout("tcp", p.addr, p.timeout)
	up := err == nil
	if up {
		_ = conn.Close()
	}

	p.mu.Lock()
	p.lastUp = up
	p.lastCheck = time.Now()
	p.mu.Unlock()
	return up
}

// markDown invalidates a cached "up" so requests right after an upstream
// failure see the starting page instead of piling onto a dead port.
func (p *probe) markDown() {
	p.mu.Lock()
	p.lastUp = false
	p.lastCheck = time.Now()
	p.mu.Unlock()
}
