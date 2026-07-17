package switcher_test

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jellehuibregtse/marquee/internal/gitinfo"
	"github.com/jellehuibregtse/marquee/internal/proxy"
	"github.com/jellehuibregtse/marquee/internal/switcher"
	"github.com/jellehuibregtse/marquee/internal/switching"
)

const testToken = "0123456789abcdef0123456789abcdef"

// fakeChild is a ChildController with no real process: it records every Restart
// so a test can assert exactly what process action a request did — or, for the
// abuse tests, did not — trigger, and its liveness and exit stream are scripted
// per target directory so a test can model a target that fails to boot, a stale
// listener that answers while the child is dead, or a mid-switch crash.
type fakeChild struct {
	mu sync.Mutex

	dirs        []string
	restartErrs map[string]error // dir -> error returned by Restart
	aliveByDir  map[string]bool  // dir -> Alive() after restarting into it (default true)
	emitOnDir   map[string]bool  // dir -> push an unexpected exit when restarted into it
	current     string

	exits   chan struct{}
	block   chan struct{} // when non-nil, Restart waits on it (concurrency test)
	entered chan struct{} // closed once, when Restart is first entered
}

func newFakeChild() *fakeChild {
	return &fakeChild{
		restartErrs: map[string]error{},
		aliveByDir:  map[string]bool{},
		emitOnDir:   map[string]bool{},
		exits:       make(chan struct{}, 1),
	}
}

func (f *fakeChild) Restart(_ context.Context, dir string) error {
	f.mu.Lock()
	entered := f.entered
	f.entered = nil
	block := f.block
	f.mu.Unlock()
	if entered != nil {
		close(entered)
	}
	if block != nil {
		<-block
	}

	f.mu.Lock()
	f.dirs = append(f.dirs, dir)
	f.current = dir
	err := f.restartErrs[dir]
	emit := f.emitOnDir[dir]
	f.mu.Unlock()

	if emit {
		f.emitExit()
	}
	return err
}

func (f *fakeChild) Alive() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.aliveByDir[f.current]; ok {
		return v
	}
	return true
}

func (f *fakeChild) Exits() <-chan struct{} { return f.exits }

func (f *fakeChild) emitExit() {
	select {
	case f.exits <- struct{}{}:
	default:
	}
}

func (f *fakeChild) restarts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.dirs...)
}

func (f *fakeChild) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.dirs)
}

// fakeWorktrees is a Worktrees whose Collect result is fixed and whose Repoint
// calls are recorded.
type fakeWorktrees struct {
	mu         sync.Mutex
	snap       gitinfo.Snapshot
	collectErr error
	repointed  []string
}

func (w *fakeWorktrees) Collect(string) (gitinfo.Snapshot, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.snap, w.collectErr
}

func (w *fakeWorktrees) Repoint(dir string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.repointed = append(w.repointed, dir)
}

func (w *fakeWorktrees) calls() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.repointed...)
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
	orch    *switcher.Orchestrator
	child   *fakeChild
	wt      *fakeWorktrees
	handler *switcher.Handler
}

type harnessOpts struct {
	token      string
	snap       *gitinfo.Snapshot
	collectErr error
	health     func(context.Context) error
	switchHook string
	dir        string
}

func newHarness(t *testing.T, opts harnessOpts) *harness {
	t.Helper()
	child := newFakeChild()
	wt := &fakeWorktrees{}
	if opts.snap != nil {
		wt.snap = *opts.snap
	} else {
		wt.snap = mainCurrent(false)
	}
	wt.collectErr = opts.collectErr
	dir := opts.dir
	if dir == "" {
		dir = "/repo/main"
	}
	orch := switcher.NewOrchestrator(switcher.OrchestratorConfig{
		Child:      child,
		Worktrees:  wt,
		Health:     opts.health,
		Dir:        dir,
		Logger:     log.New(io.Discard, "", 0),
		SwitchHook: opts.switchHook,
	})
	token := opts.token
	if token == "" {
		token = testToken
	}
	h := switcher.New(switcher.Config{Token: token, Orchestrator: orch, Logger: log.New(io.Discard, "", 0)})
	mux := proxy.NewInternalMux()
	switcher.Register(mux, h)
	return &harness{mux: mux, orch: orch, child: child, wt: wt, handler: h}
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

func (h *harness) assertTerminatedNotFired(t *testing.T) {
	t.Helper()
	select {
	case <-h.orch.Terminated():
		t.Fatal("Terminated fired: the switch's child exits leaked to the shutdown path")
	case <-time.After(100 * time.Millisecond):
	}
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
			h := newHarness(t, harnessOpts{})
			rec := h.post(`{"slug":"feature"}`, tc.mutate)
			if rec.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", rec.Code)
			}
			if n := h.child.count(); n != 0 {
				t.Errorf("child restarted %d times, want 0 (no process action on a rejected request)", n)
			}
			if n := len(h.wt.calls()); n != 0 {
				t.Errorf("repoint called %d times, want 0", n)
			}
		})
	}
}

func TestOriginFallbackMatchesHostIsAllowed(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	rec := h.post(`{"slug":"feature"}`, func(r *http.Request) {
		r.Header.Set("Origin", "http://localhost")
		r.Header.Set("X-Marquee-Token", testToken)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (Origin scheme+host match Host, no Sec-Fetch-Site)", rec.Code)
	}
	if got := h.child.restarts(); len(got) != 1 || got[0] != "/repo/feature" {
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
			h := newHarness(t, harnessOpts{})
			rec := h.post(`{"slug":"feature"}`, tc.mutate)
			if rec.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", rec.Code)
			}
			if n := h.child.count(); n != 0 {
				t.Errorf("child restarted %d times, want 0", n)
			}
		})
	}
}

func TestEmptyTokenRejectsEveryRequest(t *testing.T) {
	// A process that failed to mint a token must reject even a request that
	// echoes the empty string.
	h := newHarness(t, harnessOpts{token: " "}) // configured token is a space
	rec := h.post(`{"slug":"feature"}`, func(r *http.Request) {
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		// deliberately send no token header
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if n := h.child.count(); n != 0 {
		t.Errorf("child restarted %d times, want 0", n)
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
			h := newHarness(t, harnessOpts{})
			body, _ := json.Marshal(map[string]string{"slug": slug})
			rec := h.post(string(body), sameOriginToken)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("slug %q: status = %d, want 400", slug, rec.Code)
			}
			if n := h.child.count(); n != 0 {
				t.Errorf("slug %q: child restarted %d times, want 0", slug, n)
			}
			if n := len(h.wt.calls()); n != 0 {
				t.Errorf("slug %q: repoint called %d times, want 0", slug, n)
			}
		})
	}
}

func TestMethodNotAllowed(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	req := httptest.NewRequest(http.MethodGet, "http://localhost/__marquee/switch", nil)
	req.Host = "localhost"
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /__marquee/switch = %d, want 405", rec.Code)
	}
	if n := h.child.count(); n != 0 {
		t.Errorf("child restarted %d times on a GET, want 0", n)
	}
}

func TestHostGuardEnforced(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	rec := h.post(`{"slug":"feature"}`, func(r *http.Request) {
		r.Host = "evil.com"
		sameOriginToken(r)
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("Host evil.com = %d, want 403 (guarded mux)", rec.Code)
	}
	if n := h.child.count(); n != 0 {
		t.Errorf("child restarted %d times with a forbidden Host, want 0", n)
	}
}

func TestGitCollectFailureReportsError(t *testing.T) {
	h := newHarness(t, harnessOpts{collectErr: context.DeadlineExceeded})
	rec := h.post(`{"slug":"feature"}`, sameOriginToken)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if code := errorCode(t, rec); code != "git" {
		t.Errorf("error = %q, want %q", code, "git")
	}
	if n := h.child.count(); n != 0 {
		t.Errorf("child restarted %d times on a git failure, want 0", n)
	}
}

// --- dirty safety ---

func TestDirtyRefusedWithoutConfirm(t *testing.T) {
	snap := mainCurrent(true)
	h := newHarness(t, harnessOpts{snap: &snap})
	rec := h.post(`{"slug":"feature"}`, sameOriginToken)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if code := errorCode(t, rec); code != "dirty" {
		t.Errorf("error = %q, want %q", code, "dirty")
	}
	if n := h.child.count(); n != 0 {
		t.Errorf("child restarted %d times on a dirty refusal, want 0", n)
	}
}

func TestDirtyConfirmedAllowed(t *testing.T) {
	snap := mainCurrent(true)
	h := newHarness(t, harnessOpts{snap: &snap})
	rec := h.post(`{"slug":"feature","confirm":true}`, sameOriginToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := h.child.restarts(); len(got) != 1 || got[0] != "/repo/feature" {
		t.Fatalf("restarts = %v, want [/repo/feature]", got)
	}
}

func TestDirtySwitchToMainAlwaysAllowed(t *testing.T) {
	// Current worktree is the dirty "feature"; switching back to main must be
	// allowed without confirmation.
	current := gitinfo.CurrentWorktree{Path: "/repo/feature", Slug: "feature", IsMain: false}
	snap := twoWorktreeSnapshot(true, current)
	h := newHarness(t, harnessOpts{snap: &snap, dir: "/repo/feature"})
	rec := h.post(`{"slug":"main"}`, sameOriginToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (switch to main is always allowed)", rec.Code)
	}
	if got := h.child.restarts(); len(got) != 1 || got[0] != "/repo/main" {
		t.Fatalf("restarts = %v, want [/repo/main]", got)
	}
}

// --- concurrency ---

func TestConcurrentSwitchRejected(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	block := make(chan struct{})
	entered := make(chan struct{})
	h.child.block = block
	h.child.entered = entered

	done := make(chan int, 1)
	go func() {
		rec := h.post(`{"slug":"feature"}`, sameOriginToken)
		done <- rec.Code
	}()

	// Wait until the first switch is inside Restart (busy).
	select {
	case <-entered:
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

	close(block)
	if code := <-done; code != http.StatusOK {
		t.Errorf("first switch = %d, want 200", code)
	}
	if n := h.child.count(); n != 1 {
		t.Errorf("child restarted %d times, want 1 (the rejected switch took no action)", n)
	}
}

func TestSlugReportedWhileInProgress(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	block := make(chan struct{})
	entered := make(chan struct{})
	h.child.block = block
	h.child.entered = entered

	if got := h.orch.Progress().Slug; got != "" {
		t.Fatalf("Progress slug = %q before any switch, want empty", got)
	}
	go func() { h.post(`{"slug":"feature"}`, sameOriginToken) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("switch never reached Restart")
	}
	if got := h.orch.Progress().Slug; got != "feature" {
		t.Errorf("Progress slug = %q during switch, want %q", got, "feature")
	}
	close(block)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if h.orch.Progress().Slug == "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("Progress slug = %q after switch, want empty", h.orch.Progress().Slug)
}

// --- Switch decision logic (fast, through the orchestrator with a fake child) ---

func TestHappySwitchRestartsRepointsAndReportsPhases(t *testing.T) {
	var healthCalled bool
	h := newHarness(t, harnessOpts{health: func(context.Context) error { healthCalled = true; return nil }})

	rec := h.post(`{"slug":"feature"}`, sameOriginToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	if got := h.child.restarts(); len(got) != 1 || got[0] != "/repo/feature" {
		t.Fatalf("restarts = %v, want [/repo/feature]", got)
	}
	if got := h.wt.calls(); len(got) != 1 || got[0] != "/repo/feature" {
		t.Fatalf("repoint calls = %v, want [/repo/feature]", got)
	}
	if !healthCalled {
		t.Error("health probe was not called on a successful switch")
	}

	var body struct {
		OK     bool   `json:"ok"`
		Slug   string `json:"slug"`
		Path   string `json:"path"`
		IsMain bool   `json:"isMain"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || body.Slug != "feature" || body.Path != "/repo/feature" || body.IsMain {
		t.Errorf("response = %+v, want ok slug=feature path=/repo/feature isMain=false", body)
	}

	// The happy path steps through stopping, booting, probing, then idle.
	wantPhases := []switching.Phase{switching.Stopping, switching.Booting, switching.Probing, switching.Idle}
	assertPhases(t, h.orch.Timeline(), wantPhases)
	h.assertTerminatedNotFired(t)
}

func TestRestartFailureRevertsAndReportsBothFailed(t *testing.T) {
	// Every Restart fails: the target restart fails and so does the revert. The
	// switch reports failure (never a fake ok), never repoints, and never fires
	// Terminated (the child is down but the user can retry).
	h := newHarness(t, harnessOpts{})
	h.child.restartErrs["/repo/feature"] = context.DeadlineExceeded
	h.child.restartErrs["/repo/main"] = context.DeadlineExceeded
	h.child.aliveByDir["/repo/feature"] = false
	h.child.aliveByDir["/repo/main"] = false

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
	if n := h.child.count(); n != 2 {
		t.Errorf("child restarted %d times, want 2 (target + revert attempt)", n)
	}
	if n := len(h.wt.calls()); n != 0 {
		t.Errorf("repoint called %d times after a failed restart, want 0", n)
	}
	h.assertTerminatedNotFired(t)
}

func TestHealthFailureRevertsToPreviousWorktree(t *testing.T) {
	// The target restart succeeds but never becomes healthy; the revert to the
	// previous worktree does. Report failure with reverted:true and repoint back
	// to the previous worktree, not the target.
	h := newHarness(t, harnessOpts{
		health: func(context.Context) error { return nil },
	})
	// The health func is shared, so model the target's failure via aliveByDir:
	// the target's child is dead, the revert's is alive.
	h.child.aliveByDir["/repo/feature"] = false

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
	if got := h.child.restarts(); len(got) != 2 || got[0] != "/repo/feature" || got[1] != "/repo/main" {
		t.Errorf("restarts = %v, want [/repo/feature /repo/main]", got)
	}
	if got := h.wt.calls(); len(got) != 1 || got[0] != "/repo/main" {
		t.Errorf("repoint calls = %v, want [/repo/main]", got)
	}
	h.assertTerminatedNotFired(t)
}

// A switch whose target restart and health probe both "succeed" but whose child
// has already exited (the stale-listener case: the probe connected to a remnant
// of the old child) must be treated as a failure and revert — never a fake
// ok:true. Alive is false for the target and true for the revert.
func TestSwitchFailsWhenChildDiesDespiteHealthOK(t *testing.T) {
	h := newHarness(t, harnessOpts{
		health: func(context.Context) error { return nil }, // stale listener answers
	})
	h.child.aliveByDir["/repo/feature"] = false

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
	if got := h.child.restarts(); len(got) != 2 || got[0] != "/repo/feature" || got[1] != "/repo/main" {
		t.Errorf("restarts = %v, want [/repo/feature /repo/main]", got)
	}
	if got := h.wt.calls(); len(got) != 1 || got[0] != "/repo/main" {
		t.Errorf("repoint calls = %v, want [/repo/main] (never repoint to a dead target)", got)
	}
	h.assertTerminatedNotFired(t)
}

// A failing switch hook fails the switch before the child is ever stopped, so it
// restarts nothing — not the target, and not a needless bounce of the healthy
// previous worktree — and reports failure with reverted:true. The child is left
// untouched and alive, so Terminated stays silent.
func TestSwitchHookFailureLeavesChildUntouched(t *testing.T) {
	h := newHarness(t, harnessOpts{switchHook: "exit 1"})
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
		t.Error("a failed hook reported ok:true")
	}
	if !body.Reverted {
		t.Error("reverted = false, but the child was left on the previous, working worktree")
	}
	if n := h.child.count(); n != 0 {
		t.Errorf("child restarted %d times, want 0 (hook failed before any process action)", n)
	}
	if n := len(h.wt.calls()); n != 0 {
		t.Errorf("repoint called %d times, want 0", n)
	}
	h.assertTerminatedNotFired(t)
}

// An unexpected child exit that arrives WHILE a switch is in flight belongs to
// the switch, not the shutdown path: the orchestrator converts it into the
// revert and never fires Terminated. The doomed target emits an exit the moment
// it is restarted into; the revert comes up alive.
func TestUnexpectedExitDuringSwitchRevertsWithoutShutdown(t *testing.T) {
	h := newHarness(t, harnessOpts{
		health: func(context.Context) error { return nil },
	})
	h.child.emitOnDir["/repo/feature"] = true
	h.child.aliveByDir["/repo/feature"] = false

	rec := h.post(`{"slug":"feature"}`, sameOriginToken)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Reverted bool `json:"reverted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Reverted {
		t.Error("reverted = false, but the previous worktree came up alive")
	}
	if got := h.child.restarts(); len(got) != 2 || got[1] != "/repo/main" {
		t.Errorf("restarts = %v, want a revert to /repo/main", got)
	}
	h.assertTerminatedNotFired(t)
}

// When idle (no switch in flight), an unexpected child exit is the app dying on
// its own: the orchestrator forwards it outward as Terminated so main shuts
// down.
func TestIdleUnexpectedExitFiresTerminated(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	// The child is not alive and no switch is running: a death now is terminal.
	h.child.aliveByDir[""] = false
	h.child.emitExit()

	select {
	case <-h.orch.Terminated():
	case <-time.After(2 * time.Second):
		t.Fatal("Terminated never fired for an unexpected exit while idle")
	}
}

func assertPhases(t *testing.T, timeline []switcher.Transition, want []switching.Phase) {
	t.Helper()
	var got []switching.Phase
	for _, tr := range timeline {
		got = append(got, tr.Phase)
	}
	if len(got) != len(want) {
		t.Fatalf("phases = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("phases = %v, want %v", got, want)
		}
	}
	// Timestamps are monotonic non-decreasing (the cheap timing hook).
	for i := 1; i < len(timeline); i++ {
		if timeline[i].At.Before(timeline[i-1].At) {
			t.Errorf("transition %d timestamp %v is before %v", i, timeline[i].At, timeline[i-1].At)
		}
	}
}

// gitCmd, evalDir are shared with the real-runner integration tests.
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
