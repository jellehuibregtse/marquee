//go:build darwin || linux

package switcher_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jellehuibregtse/marquee/internal/gitinfo"
	"github.com/jellehuibregtse/marquee/internal/proxy"
	"github.com/jellehuibregtse/marquee/internal/runner"
	"github.com/jellehuibregtse/marquee/internal/switcher"
)

// The integration tests drive the *real* runner through the switch flow rather
// than a fakeRunner, so they exercise the actual process lifecycle the shutdown
// path in cmd/marquee reacts to. The child is this test binary re-executed in
// "child mode": it boots (binds a TCP port, the real health signal) only when
// the worktree it starts in contains a "boot-ok" marker, mirroring a worktree
// whose deps are installed. A worktree without the marker exits immediately —
// the real-world "switched into a worktree whose dev server fails to boot".
func TestMain(m *testing.M) {
	if os.Getenv("MARQUEE_TEST_CHILD") == "boot" {
		runTestChild()
		return
	}
	os.Exit(m.Run())
}

func runTestChild() {
	if _, err := os.Stat("boot-ok"); err != nil {
		// This worktree is not bootable (deps missing): fail to start, exactly
		// as a real dev server would when it cannot run.
		os.Exit(1)
	}
	if cwdLog := os.Getenv("CWD_LOG"); cwdLog != "" {
		wd, _ := os.Getwd()
		// #nosec G304 -- CWD_LOG is a test-controlled temp path, never HTTP input.
		if f, err := os.OpenFile(cwdLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_, _ = fmt.Fprintln(f, wd)
			_ = f.Close()
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+os.Getenv("PORT"))
	if err != nil {
		os.Exit(2)
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_ = conn.Close()
	}
}

// intHarness wires the real runner and switcher against a real git repo.
type intHarness struct {
	mux    http.Handler
	child  *runner.Runner
	port   string
	cwdLog string
	main   string
	target string
}

func freeTestPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick free port: %v", err)
	}
	defer func() { _ = ln.Close() }()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	return port
}

// newIntHarness builds a git repo with a main worktree and one extra worktree,
// both checked out from a commit that contains a "boot-ok" marker, then starts
// the real child in main and wires a real switcher over it. The caller may
// delete boot-ok from a worktree to make it fail to boot.
func newIntHarness(t *testing.T) *intHarness {
	return newIntHarnessHook(t, "")
}

func newIntHarnessHook(t *testing.T, switchHook string) *intHarness {
	t.Helper()
	main := evalDir(t)
	gitCmd(t, main, "init", "-b", "trunk")
	gitCmd(t, main, "config", "user.name", "Fixture Author")
	gitCmd(t, main, "config", "user.email", "fixture@example.com")
	gitCmd(t, main, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(main, "boot-ok"), []byte("ok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, main, "add", ".")
	gitCmd(t, main, "commit", "-m", "Add boot marker")
	target := filepath.Join(evalDir(t), "feature")
	gitCmd(t, main, "worktree", "add", "-b", "feature", target)

	port := freeTestPort(t)
	cwdLog := filepath.Join(t.TempDir(), "cwd.log")
	child := runner.New(
		[]string{os.Args[0]},
		[]string{"MARQUEE_TEST_CHILD=boot", "PORT=" + port, "CWD_LOG=" + cwdLog},
		main,
	)
	if err := child.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = child.Stop(ctx, nil)
	})

	addr := "127.0.0.1:" + port
	hctx, hcancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer hcancel()
	if err := runner.WaitTCP(hctx, addr, 20*time.Millisecond); err != nil {
		t.Fatalf("initial child never became healthy: %v", err)
	}

	sw := switcher.New(switcher.Config{
		Token:         testToken,
		Runner:        child,
		Collect:       gitinfo.Collect,
		Health:        func(ctx context.Context) error { return runner.WaitTCP(ctx, addr, 20*time.Millisecond) },
		Dir:           main,
		Logger:        log.New(io.Discard, "", 0),
		HealthTimeout: 1500 * time.Millisecond,
		SwitchHook:    switchHook,
		HookTimeout:   10 * time.Second,
	})
	mux := proxy.NewInternalMux()
	switcher.Register(mux, sw)

	return &intHarness{mux: mux, child: child, port: port, cwdLog: cwdLog, main: main, target: target}
}

func (h *intHarness) switchTo(slug string, confirm bool) *httptest.ResponseRecorder {
	body := fmt.Sprintf(`{"slug":%q,"confirm":%t}`, slug, confirm)
	req := httptest.NewRequest(http.MethodPost, "http://localhost/__marquee/switch", strings.NewReader(body))
	req.Host = "localhost"
	req.Header.Set("Content-Type", "application/json")
	sameOriginToken(req)
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec
}

// assertShutdownNotTriggered fails if the runner's terminal-exit signal — the
// exact channel cmd/marquee selects on to shut marquee down — has fired.
func (h *intHarness) assertShutdownNotTriggered(t *testing.T) {
	t.Helper()
	select {
	case <-h.child.Terminated():
		t.Fatal("shutdown path triggered: Terminated fired during/after a switch")
	case <-time.After(300 * time.Millisecond):
	}
}

func (h *intHarness) lastChildCwd(t *testing.T) string {
	t.Helper()
	dirs := h.childCwds(t)
	return dirs[len(dirs)-1]
}

// childCwds returns every directory the child successfully booted in, in order,
// symlink-resolved. A worktree that fails to boot never reaches the logging
// line, so its path never appears — which is exactly how a test proves the
// child was never (re)started in a given worktree.
func (h *intHarness) childCwds(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(h.cwdLog)
	if err != nil {
		t.Fatalf("read cwd log: %v", err)
	}
	var dirs []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		dirs = append(dirs, mustEvalSymlinks(t, line))
	}
	return dirs
}

func mustEvalSymlinks(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", p, err)
	}
	return r
}

func (h *intHarness) assertChildHealthy(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runner.WaitTCP(ctx, "127.0.0.1:"+h.port, 20*time.Millisecond); err != nil {
		t.Fatalf("child is not healthy: %v", err)
	}
	if st := h.child.Status().State; st != runner.StateRunning {
		t.Fatalf("child state = %q, want running", st)
	}
}

func (h *intHarness) assertChildDown(t *testing.T) {
	t.Helper()
	if st := h.child.Status().State; st == runner.StateRunning {
		t.Fatalf("child state = %q, want a non-running state after a fully failed switch", st)
	}
}

// Contract 1: a healthy switch keeps marquee running, now serving the new
// worktree. The transient stop of the restart must not trigger shutdown.
func TestIntegrationHealthySwitchKeepsMarqueeUp(t *testing.T) {
	h := newIntHarness(t)
	rec := h.switchTo("feature", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	h.assertChildHealthy(t)
	if got := h.lastChildCwd(t); got != mustEvalSymlinks(t, h.target) {
		t.Errorf("running child cwd = %q, want the feature worktree %q", got, h.target)
	}
	h.assertShutdownNotTriggered(t)
}

// Contract 2: a switch into a worktree that fails to boot must not exit
// marquee. The switcher reverts to the previous worktree, the reverted child is
// healthy, and the response reports failure — never a fake ok:true.
func TestIntegrationFailedSwitchRevertsAndStaysAlive(t *testing.T) {
	h := newIntHarness(t)
	// Make the target worktree unbootable (deps missing).
	if err := os.Remove(filepath.Join(h.target, "boot-ok")); err != nil {
		t.Fatal(err)
	}

	rec := h.switchTo("feature", false)
	if rec.Code/100 == 2 {
		t.Fatalf("status = %d, want a non-2xx failure; body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		OK       bool   `json:"ok"`
		Error    string `json:"error"`
		Reverted bool   `json:"reverted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.OK {
		t.Error("a failed switch reported ok:true")
	}
	if body.Error != "switch_failed" {
		t.Errorf("error = %q, want %q", body.Error, "switch_failed")
	}
	if !body.Reverted {
		t.Error("reverted = false, but the previous worktree was healthy and should have been restored")
	}
	h.assertShutdownNotTriggered(t)
	h.assertChildHealthy(t)
	if got := h.lastChildCwd(t); got != mustEvalSymlinks(t, h.main) {
		t.Errorf("running child cwd = %q, want the reverted main worktree %q", got, h.main)
	}
}

// Contract 3: when both the target and the revert fail to boot, marquee still
// does not hard-exit. The child is down, the response reports failure with
// reverted:false, and the shutdown path is not triggered so the user can retry.
func TestIntegrationBothFailStaysAlive(t *testing.T) {
	h := newIntHarness(t)
	// Neither worktree can boot: the target is missing its marker, and so is
	// the previous worktree we would revert to.
	if err := os.Remove(filepath.Join(h.target, "boot-ok")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(h.main, "boot-ok")); err != nil {
		t.Fatal(err)
	}

	// The current (main) worktree is now dirty (a tracked file was deleted), so
	// switching to a non-main target requires confirm=true.
	rec := h.switchTo("feature", true)
	if rec.Code/100 == 2 {
		t.Fatalf("status = %d, want a non-2xx failure; body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		OK       bool   `json:"ok"`
		Error    string `json:"error"`
		Reverted bool   `json:"reverted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.OK {
		t.Error("a failed switch reported ok:true")
	}
	if body.Error != "switch_failed" {
		t.Errorf("error = %q, want %q", body.Error, "switch_failed")
	}
	if body.Reverted {
		t.Error("reverted = true, but the revert also failed to boot")
	}
	h.assertShutdownNotTriggered(t)
	h.assertChildDown(t)
}

// A failing hook must revert through the existing revert path without ever
// restarting the child in the target. Uses the fakeRunner so the exact restart
// sequence is observable: only the revert into the previous worktree, never the
// target, and the managed window stays balanced.
func TestSwitchHookFailureRevertsWithoutTargetRestart(t *testing.T) {
	runner := &fakeRunner{}
	repoint := &repointTracker{}
	h := newHarness(t, switcher.Config{
		Runner:     runner,
		Repoint:    repoint.repoint,
		Dir:        "/repo/main",
		SwitchHook: "exit 1",
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
		t.Error("reverted = false, but the revert into the previous worktree should succeed")
	}
	// The hook failed before any Restart(target); only the revert restarts.
	if got := runner.restarts(); len(got) != 1 || got[0] != "/repo/main" {
		t.Errorf("restarts = %v, want [/repo/main] (target never restarted; revert only)", got)
	}
	if got := repoint.calls(); len(got) != 1 || got[0] != "/repo/main" {
		t.Errorf("repoint calls = %v, want [/repo/main]", got)
	}
	if runner.managedDepth() != 0 {
		t.Errorf("managed depth = %d after serve, want 0 (window must be balanced)", runner.managedDepth())
	}
}

// Contract 4: the switch hook runs in the TARGET worktree (its cwd is git's
// worktree path) before the child restarts there. A hook that writes a relative
// marker file proves both: the marker lands in the target worktree, and the
// switch then boots the child there successfully.
func TestIntegrationSwitchHookRunsInTargetWorktree(t *testing.T) {
	h := newIntHarnessHook(t, "echo hooked > hook-marker")
	rec := h.switchTo("feature", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	marker := filepath.Join(h.target, "hook-marker")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("hook marker not found in target worktree %q: %v", h.target, err)
	}
	h.assertChildHealthy(t)
	if got := h.lastChildCwd(t); got != mustEvalSymlinks(t, h.target) {
		t.Errorf("running child cwd = %q, want the feature worktree %q", got, h.target)
	}
	h.assertShutdownNotTriggered(t)
}

// Contract 5: a failing switch hook fails the switch and reverts, reusing the
// same revert path a failed boot uses. The child is never (re)started in the
// target, the previous worktree is restored and healthy, the response is
// switch_failed with reverted:true, and marquee stays alive.
func TestIntegrationFailingSwitchHookRevertsAndStaysAlive(t *testing.T) {
	h := newIntHarnessHook(t, "exit 1")
	rec := h.switchTo("feature", false)
	if rec.Code/100 == 2 {
		t.Fatalf("status = %d, want a non-2xx failure; body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		OK       bool   `json:"ok"`
		Error    string `json:"error"`
		Reverted bool   `json:"reverted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.OK {
		t.Error("a failed hook reported ok:true")
	}
	if body.Error != "switch_failed" {
		t.Errorf("error = %q, want %q", body.Error, "switch_failed")
	}
	if !body.Reverted {
		t.Error("reverted = false, but the previous worktree was healthy and should have been restored")
	}
	h.assertShutdownNotTriggered(t)
	h.assertChildHealthy(t)
	if got := h.lastChildCwd(t); got != mustEvalSymlinks(t, h.main) {
		t.Errorf("running child cwd = %q, want the reverted main worktree %q", got, h.main)
	}
	// The hook failed before any restart, so the child never booted in the
	// target worktree at all.
	for _, dir := range h.childCwds(t) {
		if dir == mustEvalSymlinks(t, h.target) {
			t.Errorf("child booted in target %q, but the hook should have failed before any restart", h.target)
		}
	}
}

// Contract 6: the hook runs only for the forward switch, never on the revert
// leg. The hook appends to an absolute log and the target is made unbootable so
// the boot (not the hook) fails and forces a revert. The hook must have run
// exactly once — the revert into the previously-working worktree does not
// re-run it.
func TestIntegrationSwitchHookNotRunOnRevert(t *testing.T) {
	hookLog := filepath.Join(t.TempDir(), "hook.log")
	h := newIntHarnessHook(t, "echo ran >> "+hookLog)
	// Make the target unbootable so the boot fails after a successful hook.
	if err := os.Remove(filepath.Join(h.target, "boot-ok")); err != nil {
		t.Fatal(err)
	}

	rec := h.switchTo("feature", false)
	if rec.Code/100 == 2 {
		t.Fatalf("status = %d, want a non-2xx failure; body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Reverted bool `json:"reverted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Reverted {
		t.Error("reverted = false, but the previous worktree was healthy")
	}
	h.assertChildHealthy(t)

	b, err := os.ReadFile(hookLog)
	if err != nil {
		t.Fatalf("read hook log: %v", err)
	}
	if runs := len(strings.Fields(strings.TrimSpace(string(b)))); runs != 1 {
		t.Errorf("hook ran %d times, want 1 (forward switch only, never on the revert)", runs)
	}
}
