package switcher_test

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jellehuibregtse/marquee/internal/gitinfo"
	"github.com/jellehuibregtse/marquee/internal/proxy"
	"github.com/jellehuibregtse/marquee/internal/switcher"
)

const testToken = "0123456789abcdef0123456789abcdef"

// fakeRunner records every Restart so a test can assert exactly what process
// action a request did — or, for the abuse tests, did not — trigger.
type fakeRunner struct {
	mu       sync.Mutex
	dirs     []string
	err      error
	block    chan struct{} // when non-nil, Restart waits on it (concurrency test)
	entered  chan struct{}
	managed  int  // current managed-window depth
	balanced bool // set true whenever depth returns to 0 after being >0
}

func (f *fakeRunner) Restart(_ context.Context, dir string) error {
	if f.entered != nil {
		close(f.entered)
	}
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dirs = append(f.dirs, dir)
	return f.err
}

func (f *fakeRunner) BeginManaged() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.managed++
}

func (f *fakeRunner) EndManaged() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.managed > 0 {
		f.managed--
	}
	if f.managed == 0 {
		f.balanced = true
	}
}

func (f *fakeRunner) managedDepth() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.managed
}

func (f *fakeRunner) restarts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.dirs...)
}

func (f *fakeRunner) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.dirs)
}

// repointTracker records the directories the pollers were repointed to.
type repointTracker struct {
	mu   sync.Mutex
	dirs []string
}

func (rt *repointTracker) repoint(dir string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.dirs = append(rt.dirs, dir)
}

func (rt *repointTracker) calls() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return append([]string(nil), rt.dirs...)
}

// twoWorktreeSnapshot is a clean repo whose main worktree is /repo/main and
// whose only other worktree is /repo/feature.
func twoWorktreeSnapshot(dirty bool, current gitinfo.CurrentWorktree) gitinfo.Snapshot {
	return gitinfo.Snapshot{
		Dirty:    dirty,
		Worktree: current,
		Worktrees: []gitinfo.Worktree{
			{Slug: "main", Path: "/repo/main", Branch: "trunk"},
			{Slug: "feature", Path: "/repo/feature", Branch: "feature"},
		},
	}
}

func mainCurrent(dirty bool) gitinfo.Snapshot {
	return twoWorktreeSnapshot(dirty, gitinfo.CurrentWorktree{Path: "/repo/main", Slug: "main", IsMain: true})
}

type harness struct {
	mux     http.Handler
	handler *switcher.Handler
	runner  *fakeRunner
	repoint *repointTracker
}

func newHarness(t *testing.T, cfg switcher.Config) *harness {
	t.Helper()
	runner := &fakeRunner{}
	repoint := &repointTracker{}
	if cfg.Token == "" {
		cfg.Token = testToken
	}
	if cfg.Runner == nil {
		cfg.Runner = runner
	}
	if cfg.Collect == nil {
		cfg.Collect = func(string) (gitinfo.Snapshot, error) { return mainCurrent(false), nil }
	}
	if cfg.Repoint == nil {
		cfg.Repoint = repoint.repoint
	}
	cfg.Logger = log.New(io.Discard, "", 0)
	h := switcher.New(cfg)
	mux := proxy.NewInternalMux()
	switcher.Register(mux, h)
	return &harness{mux: mux, handler: h, runner: runner, repoint: repoint}
}

func (h *harness) post(body string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "http://localhost/__marquee/switch", strings.NewReader(body))
	req.Host = "localhost"
	req.Header.Set("Content-Type", "application/json")
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec
}

func sameOriginToken(r *http.Request) {
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("X-Marquee-Token", testToken)
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %s", rec.Body.String())
	}
	return body.Error
}

// --- guard / abuse tests: assert the status AND that no process action ran ---

func TestCrossOriginRejectedNoProcessAction(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*http.Request)
	}{
		{"cross-site with valid token", func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", "cross-site")
			r.Header.Set("X-Marquee-Token", testToken)
		}},
		{"same-site (cross-origin) with valid token", func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", "same-site")
			r.Header.Set("X-Marquee-Token", testToken)
		}},
		{"sec-fetch none is not accepted for switch", func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", "none")
			r.Header.Set("X-Marquee-Token", testToken)
		}},
		{"no sec-fetch and no origin", func(r *http.Request) {
			r.Header.Set("X-Marquee-Token", testToken)
		}},
		{"origin does not match host", func(r *http.Request) {
			r.Header.Set("Origin", "http://evil.example.test")
			r.Header.Set("X-Marquee-Token", testToken)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, switcher.Config{})
			rec := h.post(`{"slug":"feature"}`, tc.mutate)
			if rec.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", rec.Code)
			}
			if n := h.runner.count(); n != 0 {
				t.Errorf("runner restarted %d times, want 0 (no process action on a rejected request)", n)
			}
			if n := len(h.repoint.calls()); n != 0 {
				t.Errorf("repoint called %d times, want 0", n)
			}
		})
	}
}

func TestOriginFallbackMatchesHostIsAllowed(t *testing.T) {
	h := newHarness(t, switcher.Config{})
	rec := h.post(`{"slug":"feature"}`, func(r *http.Request) {
		r.Header.Set("Origin", "http://localhost")
		r.Header.Set("X-Marquee-Token", testToken)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (Origin scheme+host match Host, no Sec-Fetch-Site)", rec.Code)
	}
	if got := h.runner.restarts(); len(got) != 1 || got[0] != "/repo/feature" {
		t.Fatalf("restarts = %v, want [/repo/feature]", got)
	}
}

func TestMissingOrWrongTokenRejectedNoProcessAction(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*http.Request)
	}{
		{"missing token", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-origin") }},
		{"wrong token", func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.Header.Set("X-Marquee-Token", "not-the-token")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, switcher.Config{})
			rec := h.post(`{"slug":"feature"}`, tc.mutate)
			if rec.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", rec.Code)
			}
			if n := h.runner.count(); n != 0 {
				t.Errorf("runner restarted %d times, want 0", n)
			}
		})
	}
}

func TestEmptyTokenRejectsEveryRequest(t *testing.T) {
	// A process that failed to mint a token must reject even a request that
	// echoes the empty string.
	h := newHarness(t, switcher.Config{Token: " "}) // configured token is a space
	rec := h.post(`{"slug":"feature"}`, func(r *http.Request) {
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		// deliberately send no token header
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if n := h.runner.count(); n != 0 {
		t.Errorf("runner restarted %d times, want 0", n)
	}
}

func TestUnknownOrTraversalSlugRejectedNoProcessAction(t *testing.T) {
	for _, slug := range []string{
		"nope",
		"../evil",
		"../../etc",
		"/absolute/path",
		"..%2f..%2fetc",
		"feature/..",
		".",
		"",
	} {
		t.Run(slug, func(t *testing.T) {
			h := newHarness(t, switcher.Config{})
			body, _ := json.Marshal(map[string]string{"slug": slug})
			rec := h.post(string(body), sameOriginToken)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("slug %q: status = %d, want 400", slug, rec.Code)
			}
			if n := h.runner.count(); n != 0 {
				t.Errorf("slug %q: runner restarted %d times, want 0", slug, n)
			}
			if n := len(h.repoint.calls()); n != 0 {
				t.Errorf("slug %q: repoint called %d times, want 0", slug, n)
			}
		})
	}
}

func TestMethodNotAllowed(t *testing.T) {
	h := newHarness(t, switcher.Config{})
	req := httptest.NewRequest(http.MethodGet, "http://localhost/__marquee/switch", nil)
	req.Host = "localhost"
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /__marquee/switch = %d, want 405", rec.Code)
	}
	if n := h.runner.count(); n != 0 {
		t.Errorf("runner restarted %d times on a GET, want 0", n)
	}
}

func TestHostGuardEnforced(t *testing.T) {
	h := newHarness(t, switcher.Config{})
	rec := h.post(`{"slug":"feature"}`, func(r *http.Request) {
		r.Host = "evil.com"
		sameOriginToken(r)
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("Host evil.com = %d, want 403 (guarded mux)", rec.Code)
	}
	if n := h.runner.count(); n != 0 {
		t.Errorf("runner restarted %d times with a forbidden Host, want 0", n)
	}
}

// --- dirty safety ---

func TestDirtyRefusedWithoutConfirm(t *testing.T) {
	h := newHarness(t, switcher.Config{
		Collect: func(string) (gitinfo.Snapshot, error) { return mainCurrent(true), nil },
	})
	rec := h.post(`{"slug":"feature"}`, sameOriginToken)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if code := errorCode(t, rec); code != "dirty" {
		t.Errorf("error = %q, want %q", code, "dirty")
	}
	if n := h.runner.count(); n != 0 {
		t.Errorf("runner restarted %d times on a dirty refusal, want 0", n)
	}
}

func TestDirtyConfirmedAllowed(t *testing.T) {
	h := newHarness(t, switcher.Config{
		Collect: func(string) (gitinfo.Snapshot, error) { return mainCurrent(true), nil },
	})
	rec := h.post(`{"slug":"feature","confirm":true}`, sameOriginToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := h.runner.restarts(); len(got) != 1 || got[0] != "/repo/feature" {
		t.Fatalf("restarts = %v, want [/repo/feature]", got)
	}
}

func TestDirtySwitchToMainAlwaysAllowed(t *testing.T) {
	// Current worktree is the dirty "feature"; switching back to main must be
	// allowed without confirmation.
	current := gitinfo.CurrentWorktree{Path: "/repo/feature", Slug: "feature", IsMain: false}
	h := newHarness(t, switcher.Config{
		Collect: func(string) (gitinfo.Snapshot, error) { return twoWorktreeSnapshot(true, current), nil },
	})
	rec := h.post(`{"slug":"main"}`, sameOriginToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (switch to main is always allowed)", rec.Code)
	}
	if got := h.runner.restarts(); len(got) != 1 || got[0] != "/repo/main" {
		t.Fatalf("restarts = %v, want [/repo/main]", got)
	}
}

// --- concurrency ---

func TestConcurrentSwitchRejected(t *testing.T) {
	runner := &fakeRunner{block: make(chan struct{}), entered: make(chan struct{})}
	h := newHarness(t, switcher.Config{Runner: runner})

	done := make(chan int, 1)
	go func() {
		rec := h.post(`{"slug":"feature"}`, sameOriginToken)
		done <- rec.Code
	}()

	// Wait until the first switch is inside Restart (busy).
	select {
	case <-runner.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first switch never reached Restart")
	}

	rec := h.post(`{"slug":"main"}`, sameOriginToken)
	if rec.Code != http.StatusConflict {
		t.Errorf("second concurrent switch = %d, want 409", rec.Code)
	}
	if code := errorCode(t, rec); code != "busy" {
		t.Errorf("error = %q, want %q", code, "busy")
	}

	close(runner.block)
	if code := <-done; code != http.StatusOK {
		t.Errorf("first switch = %d, want 200", code)
	}
	if n := runner.count(); n != 1 {
		t.Errorf("runner restarted %d times, want 1 (the rejected switch took no action)", n)
	}
}

func TestSwitchingSlugReportedWhileInProgress(t *testing.T) {
	runner := &fakeRunner{block: make(chan struct{}), entered: make(chan struct{})}
	h := newHarness(t, switcher.Config{Runner: runner})

	if got := h.handler.Progress().Slug; got != "" {
		t.Fatalf("Progress slug = %q before any switch, want empty", got)
	}
	go func() { h.post(`{"slug":"feature"}`, sameOriginToken) }()
	select {
	case <-runner.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("switch never reached Restart")
	}
	if got := h.handler.Progress().Slug; got != "feature" {
		t.Errorf("Progress slug = %q during switch, want %q", got, "feature")
	}
	close(runner.block)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if h.handler.Progress().Slug == "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("Progress slug = %q after switch, want empty", h.handler.Progress().Slug)
}

// --- happy path against a real temp git repo (fresh worktree-list validation) ---

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func evalDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestValidSwitchAgainstRealRepoRestartsAndRepoints(t *testing.T) {
	main := evalDir(t)
	gitCmd(t, main, "init", "-b", "trunk")
	gitCmd(t, main, "config", "user.name", "Fixture Author")
	gitCmd(t, main, "config", "user.email", "fixture@example.com")
	gitCmd(t, main, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(main, "notes.txt"), []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, main, "add", ".")
	gitCmd(t, main, "commit", "-m", "Add notes")
	wt := filepath.Join(evalDir(t), "lantern")
	gitCmd(t, main, "worktree", "add", "-b", "lantern", wt)

	var healthCalled bool
	h := newHarness(t, switcher.Config{
		Collect: gitinfo.Collect,
		Dir:     main,
		Health:  func(context.Context) error { healthCalled = true; return nil },
	})

	rec := h.post(`{"slug":"lantern"}`, sameOriginToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	if got := h.runner.restarts(); len(got) != 1 || got[0] != wt {
		t.Fatalf("restarts = %v, want [%s]", got, wt)
	}
	if got := h.repoint.calls(); len(got) != 1 || got[0] != wt {
		t.Fatalf("repoint calls = %v, want [%s]", got, wt)
	}
	if !healthCalled {
		t.Error("health probe was not called after a successful switch")
	}

	var body struct {
		OK   bool   `json:"ok"`
		Slug string `json:"slug"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || body.Slug != "lantern" || body.Path != wt {
		t.Errorf("response = %+v, want ok slug=lantern path=%s", body, wt)
	}
}

func TestRestartFailureRevertsAndReportsFailure(t *testing.T) {
	// A runner whose every Restart fails: the target restart fails and so does
	// the revert. The switch must report failure (never a fake ok), never
	// repoint, and leave the managed window balanced.
	runner := &fakeRunner{err: context.DeadlineExceeded}
	repoint := &repointTracker{}
	h := newHarness(t, switcher.Config{Runner: runner, Repoint: repoint.repoint})
	rec := h.post(`{"slug":"feature"}`, sameOriginToken)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if code := errorCode(t, rec); code != "switch_failed" {
		t.Errorf("error = %q, want %q", code, "switch_failed")
	}
	var body struct {
		OK       bool `json:"ok"`
		Reverted bool `json:"reverted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.OK {
		t.Error("a failed switch reported ok:true")
	}
	if body.Reverted {
		t.Error("reverted reported true, but the revert also failed")
	}
	// The switch attempts the target, then the revert: two Restart calls.
	if n := runner.count(); n != 2 {
		t.Errorf("runner restarted %d times, want 2 (target + revert attempt)", n)
	}
	if n := len(repoint.calls()); n != 0 {
		t.Errorf("repoint called %d times after a failed restart, want 0", n)
	}
	if runner.managedDepth() != 0 {
		t.Errorf("managed depth = %d after serve, want 0 (window must be balanced)", runner.managedDepth())
	}
}

func TestHealthFailureRevertsToPreviousWorktree(t *testing.T) {
	// The target restart succeeds but never becomes healthy; the revert to the
	// previous worktree does. The switch must report failure with reverted:true
	// and repoint back to the previous worktree, not the target.
	runner := &fakeRunner{}
	repoint := &repointTracker{}
	healthErr := map[string]error{"/repo/feature": context.DeadlineExceeded}
	h := newHarness(t, switcher.Config{
		Runner:  runner,
		Repoint: repoint.repoint,
		Dir:     "/repo/main",
		Health: func(context.Context) error {
			// The last dir the runner restarted into decides health.
			dirs := runner.restarts()
			return healthErr[dirs[len(dirs)-1]]
		},
	})
	rec := h.post(`{"slug":"feature"}`, sameOriginToken)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if code := errorCode(t, rec); code != "switch_failed" {
		t.Errorf("error = %q, want %q", code, "switch_failed")
	}
	var body struct {
		Reverted bool `json:"reverted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Reverted {
		t.Error("reverted reported false, but the revert succeeded")
	}
	if got := runner.restarts(); len(got) != 2 || got[0] != "/repo/feature" || got[1] != "/repo/main" {
		t.Errorf("restarts = %v, want [/repo/feature /repo/main]", got)
	}
	// Only the healthy revert repoints, and only to the previous worktree.
	if got := repoint.calls(); len(got) != 1 || got[0] != "/repo/main" {
		t.Errorf("repoint calls = %v, want [/repo/main]", got)
	}
}

// A switch whose target restart and health probe both "succeed" but whose child
// has already exited (the stale-listener case: the probe connected to a remnant
// of the old child) must be treated as a failure and revert — never a fake
// ok:true. ChildAlive is false for the target and true for the revert.
func TestSwitchFailsWhenChildDiesDespiteHealthOK(t *testing.T) {
	runner := &fakeRunner{}
	repoint := &repointTracker{}
	h := newHarness(t, switcher.Config{
		Runner:  runner,
		Repoint: repoint.repoint,
		Dir:     "/repo/main",
		Health:  func(context.Context) error { return nil }, // stale listener answers
		ChildAlive: func() bool {
			dirs := runner.restarts()
			// The target's child has exited; the reverted previous child is alive.
			return dirs[len(dirs)-1] != "/repo/feature"
		},
	})
	rec := h.post(`{"slug":"feature"}`, sameOriginToken)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body %s", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "switch_failed" {
		t.Errorf("error = %q, want %q", code, "switch_failed")
	}
	var body struct {
		OK       bool `json:"ok"`
		Reverted bool `json:"reverted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.OK {
		t.Error("a switch to a dead child reported ok:true despite the health probe passing")
	}
	if !body.Reverted {
		t.Error("reverted = false, but the previous worktree's child was alive")
	}
	if got := runner.restarts(); len(got) != 2 || got[0] != "/repo/feature" || got[1] != "/repo/main" {
		t.Errorf("restarts = %v, want [/repo/feature /repo/main]", got)
	}
	if got := repoint.calls(); len(got) != 1 || got[0] != "/repo/main" {
		t.Errorf("repoint calls = %v, want [/repo/main] (never repoint to a dead target)", got)
	}
	if runner.managedDepth() != 0 {
		t.Errorf("managed depth = %d after serve, want 0", runner.managedDepth())
	}
}
