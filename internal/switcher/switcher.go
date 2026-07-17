// Package switcher serves POST /__marquee/switch: the endpoint that restarts
// the wrapped dev server in a different git worktree. The HTTP handler is a thin
// adapter — parse, same-origin and token authz, the busy and dirty-confirm
// guard rails — over the switch orchestrator, which owns the switch itself (the
// restart, the readiness gate, the revert, the slug lifetime, and the phase).
// This is the highest-risk surface in marquee — it kills and spawns processes
// and selects a working directory — so every request passes the full guard
// stack before any process action, and the target directory is only ever git's
// own worktree path, never anything derived from the request. See
// docs/security.md (Threats 3 and 4).
package switcher

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/jellehuibregtse/marquee/internal/gitinfo"
	"github.com/jellehuibregtse/marquee/internal/proxy"
)

// Config wires the switch handler to its token and the orchestrator that owns
// the switch.
type Config struct {
	// Token is the per-process CSRF token minted with crypto/rand at startup; a
	// request must echo it in the X-Marquee-Token header. Empty disables the
	// endpoint's happy path (every request fails the token check).
	Token string
	// Orchestrator owns the switch. The handler resolves and guards a request,
	// then hands the plan to it.
	Orchestrator *Orchestrator
	// Logger receives operational messages. Defaults to log.Default().
	Logger *log.Logger
}

// Handler serves POST /__marquee/switch as a thin adapter over the orchestrator.
type Handler struct {
	token  string
	orch   *Orchestrator
	logger *log.Logger

	mu   sync.Mutex // serializes switches
	busy bool
}

// New builds a switch handler. The orchestrator is a hard precondition: a nil
// one is a wiring bug in main, so fail deterministically at construction
// rather than panicking on the first switch request.
func New(cfg Config) *Handler {
	if cfg.Orchestrator == nil {
		panic("switcher: Config.Orchestrator is required")
	}
	h := &Handler{
		token:  cfg.Token,
		orch:   cfg.Orchestrator,
		logger: cfg.Logger,
	}
	if h.logger == nil {
		h.logger = log.Default()
	}
	return h
}

// Register wires POST /__marquee/switch onto the guarded mux, so it inherits
// the Host allowlist and no-store guards by construction. The method pattern
// makes the mux answer any other method with 405.
func Register(mux *proxy.InternalMux, h *Handler) {
	mux.Handle("POST /__marquee/switch", http.HandlerFunc(h.serve))
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request) {
	// Guard order, all required: same-origin, then constant-time token, then the
	// concurrency lock, then a strict slug lookup, then dirty-safety. Every
	// failure returns before any process action.
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "forbidden_origin", "cross-origin switch rejected")
		return
	}
	if !h.tokenOK(r) {
		writeError(w, http.StatusForbidden, "forbidden_token", "missing or invalid switch token")
		return
	}

	req, err := parseSwitchRequest(r)
	if err != nil || req.Slug == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "missing or malformed slug")
		return
	}

	h.mu.Lock()
	if h.busy {
		h.mu.Unlock()
		writeError(w, http.StatusConflict, "busy", "a worktree switch is already in progress")
		return
	}
	h.busy = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.busy = false
		h.mu.Unlock()
	}()

	plan, err := h.orch.Prepare(req.Slug)
	if errors.Is(err, ErrUnknownSlug) {
		writeError(w, http.StatusBadRequest, "unknown_slug", "slug is not a known worktree")
		return
	}
	if err != nil {
		h.logf("switch: reading worktree list failed: %v", err)
		writeError(w, http.StatusInternalServerError, "git", "could not read the worktree list")
		return
	}

	// A dirty current worktree needs explicit confirmation, except switching back
	// to main which is always allowed.
	if plan.Dirty && !plan.IsMain && !req.Confirm {
		writeError(w, http.StatusConflict, "dirty", "current worktree has uncommitted changes; confirm to switch")
		return
	}

	// A background context so a client that disconnects mid-switch never aborts
	// the switch and strands the child: a switch runs to a healthy target or a
	// completed revert regardless.
	writeSwitchResult(w, h.orch.Switch(context.Background(), plan))
}

// writeSwitchResult renders a switch outcome to the response. The status and
// body shapes are the HTTP contract bar.js depends on and are unchanged by the
// move to the orchestrator: a success is 200 with the target's identity; every
// failure is a 502 switch_failed carrying the reverted flag and a human message.
func writeSwitchResult(w http.ResponseWriter, res Result) {
	switch res.Outcome {
	case OutcomeSuccess:
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"slug":   res.Slug,
			"path":   res.Path,
			"isMain": res.IsMain,
		})
	case OutcomeHookFailedBeforeStart:
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":    "switch_failed",
			"reverted": true,
			"slug":     res.Slug,
			"message":  "switch-hook failed before the switch began; the dev server was left running on the previous worktree",
		})
	case OutcomeReverted:
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":    "switch_failed",
			"reverted": true,
			"slug":     res.Slug,
			"message":  "target worktree did not become healthy; reverted to the previous worktree",
		})
	case OutcomeBothFailed:
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":    "switch_failed",
			"reverted": false,
			"slug":     res.Slug,
			"message":  "target worktree did not become healthy and the revert also failed; the dev server is down — retry or switch back",
		})
	}
}

func (h *Handler) tokenOK(r *http.Request) bool {
	got := r.Header.Get("X-Marquee-Token")
	if got == "" || h.token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(h.token)) == 1
}

func (h *Handler) logf(format string, args ...any) {
	h.logger.Printf("marquee: "+format, args...)
}

type switchRequest struct {
	Slug    string `json:"slug"`
	Confirm bool   `json:"confirm"`
}

// parseSwitchRequest reads {slug, confirm} from a JSON body (what bar.js sends)
// or, as a fallback, form values. The body is length-capped: the payload is two
// tiny fields, so anything larger is not a legitimate switch.
func parseSwitchRequest(r *http.Request) (switchRequest, error) {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var req switchRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
			return switchRequest{}, err
		}
		return req, nil
	}
	if err := r.ParseForm(); err != nil {
		return switchRequest{}, err
	}
	return switchRequest{
		Slug:    r.PostFormValue("slug"),
		Confirm: r.PostFormValue("confirm") == "true",
	}, nil
}

// resolveWorktree returns the worktree whose slug exactly equals slug, and only
// when exactly one does. Zero matches (unknown slug, or any traversal shape that
// cannot equal a base-name slug) and an ambiguous duplicate slug both fail, so
// the caller never acts on an unresolved target.
func resolveWorktree(worktrees []gitinfo.Worktree, slug string) (gitinfo.Worktree, bool) {
	var found gitinfo.Worktree
	matches := 0
	for _, wt := range worktrees {
		if wt.Slug == slug {
			found = wt
			matches++
		}
	}
	if matches != 1 {
		return gitinfo.Worktree{}, false
	}
	return found, true
}

// sameOrigin is the strict same-origin gate for this process-spawning endpoint.
// Sec-Fetch-Site is set by the browser and page JS cannot forge it: only
// "same-origin" is accepted. Unlike the cosmetic toggle, "none" (a typed
// address-bar navigation) is NOT accepted here — a switch must be provably
// same-origin. When Sec-Fetch-Site is absent (older browsers, curl), it falls
// back to requiring an Origin whose scheme+host+port equals the request Host.
func sameOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin":
		return true
	case "":
		return originMatchesHost(r)
	default:
		return false
	}
}

func originMatchesHost(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return u.Scheme == scheme && strings.EqualFold(u.Host, r.Host)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": code, "message": message})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
