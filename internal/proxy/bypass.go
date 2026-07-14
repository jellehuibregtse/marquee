package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
)

// barSwitches holds the bypass switches that disable bar injection while
// proxying continues (§3.3 automation bypass): the per-process
// MARQUEE_DISABLE_BAR env (read once at startup), the runtime toggle
// endpoint, and the per-request X-Marquee: skip header (carried on the
// request context, see markBarSkip).
type barSwitches struct {
	envDisabled bool
	toggleOff   atomic.Bool
}

func newBarSwitches(envDisabled bool) *barSwitches {
	return &barSwitches{envDisabled: envDisabled}
}

// shouldInject is the single precedence rule for the three switches: env
// off is hard off (the toggle cannot re-enable it), otherwise the toggle
// state applies, and a header skip always wins for its own request.
func shouldInject(envDisabled, toggleOff, headerSkip bool) bool {
	return !envDisabled && !toggleOff && !headerSkip
}

func (s *barSwitches) allows(r *http.Request) bool {
	return shouldInject(s.envDisabled, s.toggleOff.Load(), requestSkipsBar(r))
}

type skipBarKey struct{}

// markBarSkip records the X-Marquee: skip request on the context. The
// header itself is stripped before the request goes upstream, so the
// context is the only place the injector can still see the decision.
func markBarSkip(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), skipBarKey{}, true))
}

func requestSkipsBar(r *http.Request) bool {
	if r == nil {
		return false
	}
	skip, _ := r.Context().Value(skipBarKey{}).(bool)
	return skip
}

const toggleUsage = "usage: GET /__marquee/toggle?bar=on|off (no parameter reports the current state)"

// sameOriginOrDirect reports whether a state-changing toggle request may be
// honored. Sec-Fetch-Site is set by browsers and cannot be forged by page
// JS: a typed address-bar navigation sends "none" and a same-origin fetch
// sends "same-origin", while a cross-site or same-site cross-origin page
// (the CSRF vector) sends "cross-site" or "same-site". The header is absent
// for curl and scripted use, which stays allowed — its absence is a
// hardening signal, not a gate, so the legitimate CLI/typed use never breaks.
func sameOriginOrDirect(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "none", "same-origin":
		return true
	default:
		return false
	}
}

// handleToggle serves GET /__marquee/toggle. Deliberately a GET so a human
// can type it in the address bar mid-flow; it flips only the injection
// toggle, never process state. The state-changing path (a valid bar=on|off)
// rejects clearly cross-origin requests via Sec-Fetch-Site so a hostile page
// cannot silently suppress the bar cross-site (see docs/security.md); the
// no-param state report carries no guard.
func (s *barSwitches) handleToggle(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if query.Has("bar") {
		value := query.Get("bar")
		if value != "on" && value != "off" {
			http.Error(w, toggleUsage, http.StatusBadRequest)
			return
		}
		if !sameOriginOrDirect(r) {
			http.Error(w, "cross-origin bar toggle rejected", http.StatusForbidden)
			return
		}
		s.toggleOff.Store(value == "off")
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	state := "on"
	if s.envDisabled || s.toggleOff.Load() {
		state = "off"
	}
	_, _ = fmt.Fprintf(w, "bar: %s\n", state)
	if s.envDisabled {
		_, _ = io.WriteString(w, "MARQUEE_DISABLE_BAR=1 keeps the bar off for this run; the toggle cannot re-enable it\n")
	}
}
