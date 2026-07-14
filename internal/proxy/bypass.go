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

// handleToggle serves GET /__marquee/toggle. Deliberately a GET so a human
// can type it in the address bar mid-flow; it flips only the injection
// toggle, never process state, which is why it carries no same-origin or
// token guard (see docs/security.md).
func (s *barSwitches) handleToggle(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if query.Has("bar") {
		switch query.Get("bar") {
		case "on":
			s.toggleOff.Store(false)
		case "off":
			s.toggleOff.Store(true)
		default:
			http.Error(w, toggleUsage, http.StatusBadRequest)
			return
		}
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
