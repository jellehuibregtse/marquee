// Package switcher implements POST /__marquee/switch: the endpoint that
// restarts the wrapped dev server in a different git worktree. It is the
// highest-risk surface in marquee — it kills and spawns processes and selects
// a working directory — so every request passes a full guard stack before any
// process action, and the target directory is only ever git's own worktree
// path, never anything derived from the request. See docs/security.md
// (Threats 3 and 4).
package switcher

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jellehuibregtse/marquee/internal/gitinfo"
	"github.com/jellehuibregtse/marquee/internal/proxy"
)

// Runner is the subset of the process runner the switcher needs: restart the
// child in a new working directory, and open a managed lifecycle window for the
// duration of a switch. BeginManaged/EndManaged make the child's transient
// exits during a switch (the target restart, a failed boot, the revert) belong
// to the switcher, not to main's shutdown path. Kept as an interface so tests
// can assert exactly what process action a request did (or did not) trigger.
type Runner interface {
	Restart(ctx context.Context, dir string) error
	BeginManaged()
	EndManaged()
}

// Config wires the switch handler to the process runner, the git collector,
// the poller repoint, and the health probe. Collect, Repoint and Health are
// injected (rather than importing the pollers/runner directly) so the handler
// stays unit-testable with fakes.
type Config struct {
	// Token is the per-process CSRF token minted with crypto/rand at startup;
	// a request must echo it in the X-Marquee-Token header. Empty disables the
	// endpoint's happy path (every request fails the token check).
	Token string
	// Runner restarts the child in the chosen worktree.
	Runner Runner
	// Collect reads a fresh git Snapshot from a directory. The switch validates
	// the requested slug against Collect's Worktrees — git's own worktree list
	// — never against the filesystem.
	Collect func(dir string) (gitinfo.Snapshot, error)
	// Repoint moves the git/gh pollers to the new worktree after a switch, so
	// the bar reports the worktree it actually restarted into. May be nil.
	Repoint func(dir string)
	// Health blocks until the restarted child accepts connections or ctx is
	// done. May be nil (no health wait).
	Health func(ctx context.Context) error
	// ChildAlive reports whether the wrapped child process is currently running.
	// The switch consults it right after the health probe: a TCP health probe can
	// pass by connecting to a STALE listener — an escaped daemon left by the old
	// child, still holding the internal port — so a passing probe alone does not
	// prove the NEW child actually booted. If the child has exited, the switch is
	// treated as a failure and reverted; marquee must never declare a switch
	// successful on a dead child, nor shut itself down because of a switch. May be
	// nil (the liveness check is skipped).
	ChildAlive func() bool
	// Dir is the worktree the child starts in (marquee's launch cwd).
	Dir string
	// Logger receives operational messages. Defaults to log.Default().
	Logger *log.Logger
	// RestartTimeout bounds a single restart; defaults to 30s.
	RestartTimeout time.Duration
	// HealthTimeout bounds the post-restart health wait; defaults to 30s.
	HealthTimeout time.Duration
	// SwitchHook is an optional operator-supplied command run in the target
	// worktree (cwd = git's own worktree path) before the child is restarted
	// there, so a fresh worktree can be bootstrapped (e.g. "bundle install").
	// It is CLI input, never influenced by the HTTP request or the slug, and is
	// run through "sh -c" so pipelines/&& work. Empty disables it. A failing
	// hook fails the switch and reverts, exactly like a failed boot; the hook
	// never runs on the revert leg (the previous worktree already worked).
	SwitchHook string
	// HookTimeout bounds the switch hook; defaults to 5m (bootstrapping is slow).
	HookTimeout time.Duration
}

// Handler serves POST /__marquee/switch.
type Handler struct {
	token          string
	runner         Runner
	collect        func(dir string) (gitinfo.Snapshot, error)
	repoint        func(dir string)
	health         func(ctx context.Context) error
	childAlive     func() bool
	logger         *log.Logger
	restartTimeout time.Duration
	healthTimeout  time.Duration
	switchHook     string
	hookTimeout    time.Duration

	mu         sync.Mutex // serializes switches; guards busy and currentDir
	busy       bool
	currentDir string

	// slug reports the in-progress target for the proxy's switching page;
	// stored as an atomic string so the proxy reads it without the mutex.
	slug atomic.Value
}

// New builds a switch handler.
func New(cfg Config) *Handler {
	h := &Handler{
		token:          cfg.Token,
		runner:         cfg.Runner,
		collect:        cfg.Collect,
		repoint:        cfg.Repoint,
		health:         cfg.Health,
		childAlive:     cfg.ChildAlive,
		logger:         cfg.Logger,
		currentDir:     cfg.Dir,
		restartTimeout: orDuration(cfg.RestartTimeout, 30*time.Second),
		healthTimeout:  orDuration(cfg.HealthTimeout, 30*time.Second),
		switchHook:     cfg.SwitchHook,
		hookTimeout:    orDuration(cfg.HookTimeout, 5*time.Minute),
	}
	if h.logger == nil {
		h.logger = log.Default()
	}
	h.slug.Store("")
	return h
}

// Register wires POST /__marquee/switch onto the guarded mux, so it inherits
// the Host allowlist and no-store guards by construction. The method pattern
// makes the mux answer any other method with 405.
func Register(mux *proxy.InternalMux, h *Handler) {
	mux.Handle("POST /__marquee/switch", http.HandlerFunc(h.serve))
}

// SwitchingSlug reports the slug of an in-progress switch (empty when idle),
// for proxy.Handler.SetSwitchingProbe.
func (h *Handler) SwitchingSlug() string {
	s, _ := h.slug.Load().(string)
	return s
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request) {
	// Guard order, all required: same-origin, then constant-time token, then
	// the concurrency lock, then a strict slug lookup, then dirty-safety.
	// Every failure returns before any process action.
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
	dir := h.currentDir
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.busy = false
		h.mu.Unlock()
	}()

	snap, err := h.collect(dir)
	if err != nil {
		h.logf("switch: reading worktree list failed: %v", err)
		writeError(w, http.StatusInternalServerError, "git", "could not read the worktree list")
		return
	}

	// The slug is only ever an exact-match key into git's own worktree list;
	// the absolute path comes from git's output, never from the request. An
	// unknown slug or any traversal shape simply fails to match.
	target, ok := resolveWorktree(snap.Worktrees, req.Slug)
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown_slug", "slug is not a known worktree")
		return
	}
	targetIsMain := len(snap.Worktrees) > 0 && target.Path == snap.Worktrees[0].Path

	// A dirty current worktree needs explicit confirmation, except switching
	// back to main which is always allowed.
	if snap.Dirty && !targetIsMain && !req.Confirm {
		writeError(w, http.StatusConflict, "dirty", "current worktree has uncommitted changes; confirm to switch")
		return
	}

	// prevDir is the worktree the child is running in now. If the switch fails
	// to come up healthy, the switcher reverts the child back to it, so a bad
	// switch leaves marquee exactly where it started rather than dead.
	prevDir := dir

	// The whole switch — target restart, health wait, and any revert — is a
	// managed lifecycle window. A child exit inside it belongs to the switcher,
	// never to main's shutdown path: main must not tear marquee down because a
	// switched-into dev server failed to boot.
	h.runner.BeginManaged()
	defer h.runner.EndManaged()

	h.setSwitching(target.Slug)
	defer h.setSwitching("")

	switchErr := h.switchInto(target.Path, true)
	if switchErr == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"slug":   target.Slug,
			"path":   target.Path,
			"isMain": targetIsMain,
		})
		return
	}
	h.logf("switch to %q failed: %v; reverting to %q", target.Slug, switchErr, prevDir)

	// Revert exactly once, back to the previous worktree. Whether the revert
	// itself comes up healthy or not, marquee stays alive (a dead-but-alive
	// marquee the user can retry beats one that vanished); the response always
	// reports the switch as a failure, never a fake success. The switch hook
	// does NOT run on the revert: the previous worktree already worked, so
	// re-bootstrapping it is wasteful and could fail spuriously.
	if err := h.switchInto(prevDir, false); err != nil {
		h.logf("revert to %q also failed: %v", prevDir, err)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":    "switch_failed",
			"reverted": false,
			"slug":     target.Slug,
			"message":  "target worktree did not become healthy and the revert also failed; the dev server is down — retry or switch back",
		})
		return
	}
	writeJSON(w, http.StatusBadGateway, map[string]any{
		"error":    "switch_failed",
		"reverted": true,
		"slug":     target.Slug,
		"message":  "target worktree did not become healthy; reverted to the previous worktree",
	})
}

// switchInto optionally runs the switch hook in dir, then restarts the child
// there and waits for it to become healthy. On success it repoints the pollers
// and records dir as the current worktree; on failure (the hook errored, the
// restart could not start, or the child never became healthy) it returns the
// error and leaves currentDir untouched so a revert restores it. runHook is
// true only for the forward switch into the target — never for the revert.
func (h *Handler) switchInto(dir string, runHook bool) error {
	if runHook {
		if err := h.runSwitchHook(dir); err != nil {
			return err
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), h.restartTimeout)
	defer cancel()
	if err := h.runner.Restart(ctx, dir); err != nil {
		return err
	}

	if h.health != nil {
		hctx, hcancel := context.WithTimeout(context.Background(), h.healthTimeout)
		err := h.health(hctx)
		hcancel()
		if err != nil {
			return err
		}
	}

	// A passing health probe is not proof the new child booted: with a
	// daemonizing process manager, the probe can connect to a STALE listener an
	// escaped remnant of the OLD child is still holding. Require the child to be
	// actually running, so a switch to a child that has exited is a failure (and
	// reverts) rather than a fake success that later shuts marquee down.
	if h.childAlive != nil && !h.childAlive() {
		return fmt.Errorf("child exited before becoming healthy in %s", dir)
	}

	h.mu.Lock()
	h.currentDir = dir
	h.mu.Unlock()
	if h.repoint != nil {
		h.repoint(dir)
	}
	return nil
}

// runSwitchHook runs the operator's switch hook in the target worktree so a
// fresh worktree can be bootstrapped before the child starts there. It is a
// no-op when no hook is configured. The hook's stdout and stderr are streamed
// to marquee's logger, prefixed, so the user sees bootstrap progress and
// errors; a non-zero exit or a timeout returns an error, which the caller turns
// into a reverting switch failure.
func (h *Handler) runSwitchHook(dir string) error {
	if h.switchHook == "" {
		return nil
	}
	h.logf("switch-hook: running %q in %s", h.switchHook, dir)

	ctx, cancel := context.WithTimeout(context.Background(), h.hookTimeout)
	defer cancel()

	// #nosec G204 -- switchHook is the operator's own CLI flag value (like the
	// wrapped dev command itself), never derived from the HTTP request or the
	// slug; dir is git's own worktree path, not request input. Running it via
	// "sh -c" is deliberate so operators can write pipelines and && chains.
	cmd := exec.CommandContext(ctx, "sh", "-c", h.switchHook)
	cmd.Dir = dir
	// Run the hook in its own process group and kill the whole group on
	// timeout, so a hook like "bundle install" doesn't leak its children
	// (ruby, native builds) when it hangs — mirroring how the runner reaps
	// the child. WaitDelay bounds how long we wait for I/O to drain after.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	out := &hookOutput{logf: h.logf}
	cmd.Stdout = out
	cmd.Stderr = out
	err := cmd.Run()
	out.flush()
	if err != nil {
		return fmt.Errorf("switch-hook %q failed: %w", h.switchHook, err)
	}
	return nil
}

// hookOutput forwards a subprocess's combined output to the logger one line at
// a time, each line prefixed so switch-hook progress is distinguishable in
// marquee's stderr. os/exec guarantees no concurrent Write when the same writer
// is used for both Stdout and Stderr, so no lock is needed.
type hookOutput struct {
	logf func(string, ...any)
	buf  []byte
}

func (o *hookOutput) Write(p []byte) (int, error) {
	o.buf = append(o.buf, p...)
	for {
		i := bytes.IndexByte(o.buf, '\n')
		if i < 0 {
			break
		}
		o.logf("switch-hook: %s", o.buf[:i])
		o.buf = o.buf[i+1:]
	}
	return len(p), nil
}

func (o *hookOutput) flush() {
	if len(o.buf) > 0 {
		o.logf("switch-hook: %s", o.buf)
		o.buf = nil
	}
}

func (h *Handler) tokenOK(r *http.Request) bool {
	got := r.Header.Get("X-Marquee-Token")
	if got == "" || h.token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(h.token)) == 1
}

func (h *Handler) setSwitching(slug string) { h.slug.Store(slug) }

func (h *Handler) logf(format string, args ...any) {
	h.logger.Printf("marquee: "+format, args...)
}

type switchRequest struct {
	Slug    string `json:"slug"`
	Confirm bool   `json:"confirm"`
}

// parseSwitchRequest reads {slug, confirm} from a JSON body (what bar.js
// sends) or, as a fallback, form values. The body is length-capped: the
// payload is two tiny fields, so anything larger is not a legitimate switch.
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

// resolveWorktree returns the worktree whose slug exactly equals slug, and
// only when exactly one does. Zero matches (unknown slug, or any traversal
// shape that cannot equal a base-name slug) and an ambiguous duplicate slug
// both fail, so the caller never acts on an unresolved target.
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

// sameOrigin is the strict same-origin gate for this process-spawning
// endpoint. Sec-Fetch-Site is set by the browser and page JS cannot forge it:
// only "same-origin" is accepted. Unlike the cosmetic toggle, "none" (a typed
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

func orDuration(d, fallback time.Duration) time.Duration {
	if d <= 0 {
		return fallback
	}
	return d
}
