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
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jellehuibregtse/marquee/internal/gitinfo"
	portpkg "github.com/jellehuibregtse/marquee/internal/port"
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
	switch os.Getenv("MARQUEE_TEST_CHILD") {
	case "boot":
		runTestChild()
		return
	case "daemon":
		runDaemonChild()
		return
	case "listener":
		runDetachedListener()
		return
	}
	os.Exit(m.Run())
}

// runDaemonChild mimics a process manager that daemonizes (overmind/tmux). When
// the worktree is bootstrapped and marquee's internal port is free, it launches
// a listener in its OWN session (Setsid) so the listener escapes marquee's child
// process group and survives the group-kill on stop — exactly how tmux
// daemonizes its server — then blocks so marquee sees a running child. When the
// port is already held (a previous worktree's escaped listener still squats it)
// it launches nothing and just blocks, like a manager that will not re-bind.
// When the worktree is not bootstrapped it exits non-zero after a short delay,
// long enough that marquee has already (mis)read the stale escaped listener as
// healthy — the ordering that makes the self-kill deterministic on unfixed code.
func runDaemonChild() {
	if _, err := os.Stat("boot-ok"); err != nil {
		time.Sleep(300 * time.Millisecond)
		os.Exit(1)
	}
	if !loopbackHeldTest(os.Getenv("PORT")) {
		spawnDetachedListener()
	}
	// Block like a manager's foreground client until stopped. Blocking on a
	// signal (rather than select{}) keeps Go's deadlock detector quiet and lets
	// the process-group SIGTERM end this parent cleanly while the detached
	// listener, in its own session, survives.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	os.Exit(0)
}

// spawnDetachedListener re-execs this binary as a Setsid listener, waits until
// it is accepting, then releases it. The listener is now in its own session and
// will outlive a process-group kill of this daemon parent.
func spawnDetachedListener() {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "MARQUEE_TEST_CHILD=listener")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		os.Exit(3)
	}
	addr := "127.0.0.1:" + os.Getenv("PORT")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond); err == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = cmd.Process.Release()
}

// runDetachedListener is the escaped grandchild: it binds the internal port,
// records its own PID (so the test can reap it on cleanup) and the worktree it
// booted in (so a test can tell which worktree's listener is live), then serves
// until it is killed.
func runDetachedListener() {
	port := os.Getenv("PORT")
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		os.Exit(4)
	}
	if pidLog := os.Getenv("DETACH_PID_LOG"); pidLog != "" {
		appendLineTest(pidLog, strconv.Itoa(os.Getpid()))
	}
	if cwdLog := os.Getenv("CWD_LOG"); cwdLog != "" {
		wd, _ := os.Getwd()
		appendLineTest(cwdLog, wd)
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_ = conn.Close()
	}
}

func loopbackHeldTest(port string) bool {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 100*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func appendLineTest(path, line string) {
	// #nosec G304 -- path is a test-controlled temp file, never HTTP input.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintln(f, line)
	_ = f.Close()
}

func runTestChild() {
	if _, err := os.Stat("boot-ok"); err != nil {
		// This worktree is not bootable (deps missing): fail to start, exactly
		// as a real dev server would when it cannot run.
		os.Exit(1)
	}
	// A "blocker" file stands in for a stale process-manager socket (e.g.
	// .overmind.sock) that makes a manager refuse to boot until it is cleared. A
	// switch hook such as "rm -f blocker" removes it; a worktree that still has
	// one cannot come up.
	if _, err := os.Stat("blocker"); err == nil {
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

// intHarness wires the real runner, the orchestrator, and the switch handler
// against a real git repo, so the tests exercise the actual process lifecycle
// the shutdown path in cmd/marquee reacts to.
type intHarness struct {
	mux    http.Handler
	child  *runner.Runner
	orch   *switcher.Orchestrator
	port   string
	cwdLog string
	main   string
	target string
}

// realWorktrees is the production Worktrees wiring for the integration tests:
// git's own Collect, and a no-op Repoint (the pollers are not run here).
type realWorktrees struct{}

func (realWorktrees) Collect(dir string) (gitinfo.Snapshot, error) { return gitinfo.Collect(dir) }
func (realWorktrees) Repoint(string)                               {}

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
		nil,
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
	if err := portpkg.WaitTCP(hctx, addr, 20*time.Millisecond); err != nil {
		t.Fatalf("initial child never became healthy: %v", err)
	}

	orch := switcher.NewOrchestrator(switcher.OrchestratorConfig{
		Child:         child,
		Worktrees:     realWorktrees{},
		Health:        func(ctx context.Context) error { return portpkg.WaitTCP(ctx, addr, 20*time.Millisecond) },
		Dir:           main,
		Logger:        log.New(io.Discard, "", 0),
		HealthTimeout: 1500 * time.Millisecond,
		SwitchHook:    switchHook,
		HookTimeout:   10 * time.Second,
	})
	sw := switcher.New(switcher.Config{Token: testToken, Orchestrator: orch, Logger: log.New(io.Discard, "", 0)})
	mux := proxy.NewInternalMux()
	switcher.Register(mux, sw)

	return &intHarness{mux: mux, child: child, orch: orch, port: port, cwdLog: cwdLog, main: main, target: target}
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

// assertShutdownNotTriggered fails if the orchestrator's outward terminal-exit
// signal — the exact channel cmd/marquee selects on to shut marquee down — has
// fired.
func (h *intHarness) assertShutdownNotTriggered(t *testing.T) {
	t.Helper()
	select {
	case <-h.orch.Terminated():
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
	if err := portpkg.WaitTCP(ctx, "127.0.0.1:"+h.port, 20*time.Millisecond); err != nil {
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

// Contract 6: the hook runs on BOTH legs — the forward switch and the revert.
// The hook appends to an absolute log and the target is made unbootable so the
// boot (not the hook) fails and forces a revert. The hook must have run twice:
// once bootstrapping the (doomed) target, and once again on the revert so an
// operator cleanup step can clear stale process-manager state before the
// previous worktree restarts.
func TestIntegrationSwitchHookRunsOnRevert(t *testing.T) {
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
	if runs := len(strings.Fields(strings.TrimSpace(string(b)))); runs != 2 {
		t.Errorf("hook ran %d times, want 2 (forward switch and the revert)", runs)
	}
}

// Contract 7: the revert hook is load-bearing for recovery — it reproduces the
// real incident. The forward switch stops the child, and its process manager
// has left a stale socket in the previous worktree that blocks a clean reboot
// there. Only because the revert now re-runs the hook (which clears the socket)
// does the previous worktree come back up; without it the revert restart would
// hit the blocker and both legs would fail, leaving the dev server dead.
func TestIntegrationRevertHookClearsStaleBlocker(t *testing.T) {
	h := newIntHarnessHook(t, "rm -f blocker")
	// The previous (main) worktree carries a stale blocker, as a killed process
	// manager would leave behind; the target simply cannot boot (deps missing),
	// which forces the revert.
	if err := os.WriteFile(filepath.Join(h.main, "blocker"), []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(h.target, "boot-ok")); err != nil {
		t.Fatal(err)
	}

	// main now has an untracked blocker file, so the switch to a non-main target
	// needs confirmation.
	rec := h.switchTo("feature", true)
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
		t.Error("reverted = false: the revert hook should have cleared the blocker so the previous worktree boots")
	}
	h.assertShutdownNotTriggered(t)
	h.assertChildHealthy(t)
	if got := h.lastChildCwd(t); got != mustEvalSymlinks(t, h.main) {
		t.Errorf("running child cwd = %q, want the reverted main worktree %q", got, h.main)
	}
	if _, err := os.Stat(filepath.Join(h.main, "blocker")); !os.IsNotExist(err) {
		t.Errorf("blocker still present in main worktree; the revert hook should have removed it (stat err = %v)", err)
	}
}

// newDaemonHarness wires the real runner + orchestrator over a child that
// daemonizes (MARQUEE_TEST_CHILD=daemon): its listener runs in a separate
// session and survives the process-group stop, exactly like a tmux/overmind
// server. The switch is wired as in production — a port.Reclaimer passed to the
// runner frees the internal port before the new child spawns, and the
// orchestrator's liveness gate (child.Alive) requires a live child. Cleanup
// reaps every escaped listener the run spawned, so no detached port leaks
// across the suite.
func newDaemonHarness(t *testing.T) *intHarness {
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
	portInt, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	tmp := t.TempDir()
	cwdLog := filepath.Join(tmp, "cwd.log")
	pidLog := filepath.Join(tmp, "detach-pids.log")
	child := runner.New(
		[]string{os.Args[0]},
		[]string{"MARQUEE_TEST_CHILD=daemon", "PORT=" + port, "CWD_LOG=" + cwdLog, "DETACH_PID_LOG=" + pidLog},
		main,
		portpkg.Reclaimer{Port: portInt, Logf: func(string, ...any) {}},
	)
	if err := child.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = child.Stop(ctx, nil)
		reapDetached(pidLog)
	})

	addr := "127.0.0.1:" + port
	hctx, hcancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer hcancel()
	if err := portpkg.WaitTCP(hctx, addr, 20*time.Millisecond); err != nil {
		t.Fatalf("initial child never became healthy: %v", err)
	}

	orch := switcher.NewOrchestrator(switcher.OrchestratorConfig{
		Child:         child,
		Worktrees:     realWorktrees{},
		Health:        func(ctx context.Context) error { return portpkg.WaitTCP(ctx, addr, 20*time.Millisecond) },
		Dir:           main,
		Logger:        log.New(io.Discard, "", 0),
		HealthTimeout: 700 * time.Millisecond,
	})
	sw := switcher.New(switcher.Config{Token: testToken, Orchestrator: orch, Logger: log.New(io.Discard, "", 0)})
	mux := proxy.NewInternalMux()
	switcher.Register(mux, sw)

	return &intHarness{mux: mux, child: child, orch: orch, port: port, cwdLog: cwdLog, main: main, target: target}
}

// reapDetached kills every escaped listener PID the daemon child recorded, so a
// listener that outlived the process-group stop does not leak its port.
func reapDetached(pidLog string) {
	b, err := os.ReadFile(pidLog)
	if err != nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if pid, err := strconv.Atoi(line); err == nil && pid > 1 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// Daemonizing-manager regression (the confirmed self-kill). The target is not
// bootstrapped so its child exits, but the OLD child's escaped listener still
// holds the internal port, so a naive TCP health probe connects to that stale
// listener and passes. On unfixed code the switch is declared successful, its
// managed window closes, the real (exited) child's death is then observed as
// unmanaged, Terminated fires, and marquee shuts itself down. The fix frees the
// port before the new child starts (the probe becomes honest) and requires a
// live child, so the switch is a clean, reverting failure and marquee stays up.
func TestIntegrationDaemonSelfKillRegression(t *testing.T) {
	h := newDaemonHarness(t)
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
		t.Error("switch to an exited child reported ok:true (a stale listener lied to the health probe)")
	}
	if body.Error != "switch_failed" {
		t.Errorf("error = %q, want %q", body.Error, "switch_failed")
	}
	if !body.Reverted {
		t.Error("reverted = false, but the previous worktree is bootstrapped and should be restored")
	}
	// The crux: marquee must NOT have torn itself down because of the switch.
	h.assertShutdownNotTriggered(t)
	h.assertChildHealthy(t)
	if got := h.lastChildCwd(t); got != mustEvalSymlinks(t, h.main) {
		t.Errorf("live listener cwd = %q, want the reverted main worktree %q", got, h.main)
	}
}

// With a daemonizing manager, a switch into a healthy target only works if the
// escaped listener squatting the internal port is reaped first. After the reap,
// the new child binds the freed port and truly serves the target worktree — the
// live listener is the target's, not the stale one still answering for main.
func TestIntegrationDaemonPortFreedSwitchSucceeds(t *testing.T) {
	h := newDaemonHarness(t)

	rec := h.switchTo("feature", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	h.assertChildHealthy(t)
	if got := h.lastChildCwd(t); got != mustEvalSymlinks(t, h.target) {
		t.Errorf("live listener cwd = %q, want the feature worktree %q (new child bound the freed port)", got, h.target)
	}
	h.assertShutdownNotTriggered(t)
}
