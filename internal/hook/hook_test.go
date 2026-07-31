package hook_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jellehuibregtse/marquee/internal/hook"
)

// recorder collects the lines a hook run logged, so a test can assert the
// operator actually sees the hook's output.
type recorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *recorder) logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

func (r *recorder) joined() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n")
}

// in builds the minimal invocation for the tests that only care about where the
// hook runs, not about which leg it belongs to.
func in(dir string) hook.Invocation {
	return hook.Invocation{Leg: hook.LegStart, TargetSlug: filepath.Base(dir), TargetDir: dir}
}

func TestRunUsesTheGivenWorktreeAsWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	r := hook.New(hook.Config{Command: "echo hooked > marker"})
	if err := r.Run(context.Background(), in(dir)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "marker")); err != nil {
		t.Fatalf("hook did not run with cwd %q: %v", dir, err)
	}
}

func TestRunReportsANonZeroExit(t *testing.T) {
	r := hook.New(hook.Config{Command: "exit 3"})
	err := r.Run(context.Background(), in(t.TempDir()))
	if err == nil {
		t.Fatal("Run returned nil for a hook that exited 3")
	}
	if !strings.Contains(err.Error(), "exit 3") {
		t.Errorf("error = %q, want it to name the failing command", err)
	}
}

// An unconfigured hook is a working no-op, so no caller needs to guard the call
// site. A nil Runner behaves the same, which is what a nil OrchestratorConfig.Hook
// relies on.
func TestUnconfiguredHookIsANoOp(t *testing.T) {
	dir := t.TempDir()
	if err := hook.New(hook.Config{}).Run(context.Background(), in(dir)); err != nil {
		t.Fatalf("empty hook: %v", err)
	}
	var nilRunner *hook.Runner
	if err := nilRunner.Run(context.Background(), in(dir)); err != nil {
		t.Fatalf("nil hook: %v", err)
	}
	if nilRunner.Configured() || hook.New(hook.Config{}).Configured() {
		t.Error("Configured reported true without a command")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("no-op hook touched the worktree: %v", entries)
	}
}

// A hanging hook must not hang marquee, and it must not leak the children it
// spawned: the hook leads its own process group and the whole group is killed
// when the ceiling runs out. The grandchild writes a marker after its parent's
// deadline, so a surviving group would leave it behind.
func TestTheCeilingKillsTheWholeHookProcessGroup(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "grandchild-survived")
	r := hook.New(hook.Config{Command: "sh -c 'sleep 5; touch " + marker + "' & wait", Timeout: 200 * time.Millisecond})

	start := time.Now()
	err := r.Run(context.Background(), in(dir))
	if err == nil {
		t.Fatal("Run returned nil for a hook that timed out")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Run took %s, want the timeout to cut it short", elapsed)
	}

	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("the hook's grandchild outlived the timeout: the process group was not killed")
	}
}

// A hook silent past its idle timeout is killed well inside the ceiling, and the
// kill takes the whole process group with it so nothing it spawned is left
// behind. The failure has to say silence killed it, because that names a
// different problem than running out of total time, and it still has to carry the
// hook's own last words.
//
// The durations here are scaled down; the real idle timeout is DefaultIdleTimeout.
func TestSilenceKillsTheHookAndItsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "grandchild-survived")
	var failure recorder
	r := hook.New(hook.Config{
		Command:     "echo installing; sh -c 'sleep 5; touch " + marker + "' & wait",
		IdleTimeout: 300 * time.Millisecond,
		Timeout:     30 * time.Second,
		Errf:        failure.logf,
	})

	start := time.Now()
	err := r.Run(context.Background(), in(dir))
	if err == nil {
		t.Fatal("Run returned nil for a hook that went silent past its idle timeout")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Run took %s, want the idle timeout to cut it short", elapsed)
	}
	if !strings.Contains(err.Error(), "produced no output for 300ms") {
		t.Errorf("error = %q, want it to name the silence that killed the hook", err)
	}
	if got := failure.joined(); !strings.Contains(got, "installing") {
		t.Errorf("error sink saw %q, want the tail of the killed hook's output", got)
	}

	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("the hook's grandchild outlived the idle timeout: the process group was not killed")
	}
}

// The ceiling is the other bound, and the only one a hook that keeps talking can
// ever hit. Its message must not blame silence, since nothing was silent.
func TestTheCeilingKillsAHookThatNeverStopsTalking(t *testing.T) {
	var failure recorder
	r := hook.New(hook.Config{
		Command:     "while :; do echo still going; sleep 0.05; done",
		IdleTimeout: 10 * time.Second,
		Timeout:     400 * time.Millisecond,
		Errf:        failure.logf,
	})

	start := time.Now()
	err := r.Run(context.Background(), in(t.TempDir()))
	if err == nil {
		t.Fatal("Run returned nil for a hook that outran its ceiling")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Run took %s, want the ceiling to cut it short", elapsed)
	}
	if !strings.Contains(err.Error(), "exceeded its 400ms limit") {
		t.Errorf("error = %q, want it to name the ceiling rather than silence", err)
	}
	if strings.Contains(err.Error(), "produced no output") {
		t.Errorf("error = %q blames silence for a hook that never stopped printing", err)
	}
	if got := failure.joined(); !strings.Contains(got, "still going") {
		t.Errorf("error sink saw %q, want the tail of the killed hook's output", got)
	}
}

// The defect the idle timeout replaced: a hook doing real work was killed at a
// fixed total budget, mid-build, for being slow. Every write resets the idle
// timer, so a hook that keeps printing runs to completion however long it takes,
// here several times over its own idle timeout.
func TestAHookThatKeepsPrintingIsNotKilled(t *testing.T) {
	dir := t.TempDir()
	r := hook.New(hook.Config{
		Command:     "for i in $(seq 1 12); do echo step-$i; sleep 0.1; done; echo done > finished",
		IdleTimeout: 300 * time.Millisecond,
		Timeout:     30 * time.Second,
	})

	if err := r.Run(context.Background(), in(dir)); err != nil {
		t.Fatalf("a hook that kept printing was killed anyway: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "finished")); err != nil {
		t.Fatalf("the hook did not run to completion: %v", err)
	}
}

func TestOutputIsStreamedToTheLoggerLineByLine(t *testing.T) {
	var rec recorder
	r := hook.New(hook.Config{Command: "echo first; echo second >&2; printf trailing", Logf: rec.logf})
	if err := r.Run(context.Background(), in(t.TempDir())); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := rec.joined()
	for _, want := range []string{"switch-hook: first", "switch-hook: second", "switch-hook: trailing"} {
		if !strings.Contains(got, want) {
			t.Errorf("logged output %q, want it to contain %q", got, want)
		}
	}
}

// A cancelled parent context stops the hook: the switch's own context bounds it,
// so a hook can never outlive the operation that started it.
func TestParentCancellationStopsTheHook(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err := hook.New(hook.Config{Command: "sleep 5"}).Run(ctx, in(t.TempDir()))
	if err == nil {
		t.Fatal("Run returned nil for a hook cancelled mid-flight")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Run took %s, want the cancellation to cut it short", elapsed)
	}
}

// The hook is told which worktree it is bootstrapping and which one is being
// left, through its environment, so a single script can serve all three legs.
func TestInvocationIsExportedToTheHookEnvironment(t *testing.T) {
	dir := t.TempDir()
	dump := filepath.Join(dir, "env")
	r := hook.New(hook.Config{Command: `{ echo "$MARQUEE_HOOK_LEG"; echo "$MARQUEE_TARGET_SLUG"; echo "$MARQUEE_TARGET_DIR"; echo "[$MARQUEE_PREV_DIR]"; } > ` + dump})

	inv := hook.Invocation{Leg: hook.LegRevert, TargetSlug: "main", TargetDir: dir, PrevDir: "/repo/feature"}
	if err := r.Run(context.Background(), inv); err != nil {
		t.Fatalf("Run: %v", err)
	}

	b, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(b)), "\n")
	want := []string{"revert", "main", dir, "[/repo/feature]"}
	if len(got) != len(want) {
		t.Fatalf("hook saw %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// The initial start has no previous worktree, so MARQUEE_PREV_DIR is set but
// empty: a hook can test it rather than guess whether the variable exists.
func TestPrevDirIsEmptyOnTheStartLeg(t *testing.T) {
	dir := t.TempDir()
	dump := filepath.Join(dir, "env")
	r := hook.New(hook.Config{Command: `{ echo "${MARQUEE_PREV_DIR-unset}"; echo "$MARQUEE_HOOK_LEG"; } > ` + dump})
	if err := r.Run(context.Background(), hook.Invocation{Leg: hook.LegStart, TargetSlug: "main", TargetDir: dir}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	b, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != "\nstart\n" {
		t.Errorf("hook saw %q, want an empty MARQUEE_PREV_DIR and leg %q", got, "start")
	}
}

// The hook keeps the environment marquee itself was launched with; the MARQUEE_*
// entries are additions, not a replacement.
func TestHookInheritsTheParentEnvironment(t *testing.T) {
	t.Setenv("MARQUEE_HOOK_TEST_INHERITED", "yes")
	dir := t.TempDir()
	dump := filepath.Join(dir, "env")
	r := hook.New(hook.Config{Command: `echo "$MARQUEE_HOOK_TEST_INHERITED" > ` + dump})
	if err := r.Run(context.Background(), in(dir)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	b, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(b)) != "yes" {
		t.Errorf("hook did not inherit the parent environment: %q", b)
	}
}

// A failing hook has to explain itself through the error sink, because the sink
// that streamed its progress is the one marquee's --quiet drops. Without this a
// quiet run learns that the bootstrap failed and never why.
func TestFailureIsRepeatedThroughTheErrorSink(t *testing.T) {
	var progress, failure recorder
	r := hook.New(hook.Config{
		Command: "echo working; echo REASON >&2; exit 4",
		Logf:    progress.logf,
		Errf:    failure.logf,
	})
	if err := r.Run(context.Background(), in(t.TempDir())); err == nil {
		t.Fatal("Run returned nil for a hook that exited 4")
	}
	if got := failure.joined(); !strings.Contains(got, "REASON") {
		t.Errorf("error sink saw %q, want the hook's own reason", got)
	}
	if got := progress.joined(); !strings.Contains(got, "working") {
		t.Errorf("progress sink saw %q, want the streamed output", got)
	}
}

// A hook that succeeds says nothing through the error sink, so a normal run is
// no noisier than before.
func TestSuccessIsSilentOnTheErrorSink(t *testing.T) {
	var failure recorder
	r := hook.New(hook.Config{Command: "echo fine", Errf: failure.logf})
	if err := r.Run(context.Background(), in(t.TempDir())); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := failure.joined(); got != "" {
		t.Errorf("error sink saw %q for a hook that succeeded", got)
	}
}

// The kept tail is bounded, so a chatty hook cannot grow it without limit or
// bury its own error under progress output. The last line always survives.
func TestFailureTailIsCapped(t *testing.T) {
	var failure recorder
	r := hook.New(hook.Config{
		Command: "for i in $(seq 1 400); do echo line-$i; done; echo REASON >&2; exit 1",
		Errf:    failure.logf,
	})
	if err := r.Run(context.Background(), in(t.TempDir())); err == nil {
		t.Fatal("Run returned nil for a hook that exited 1")
	}
	failure.mu.Lock()
	lines := len(failure.lines)
	failure.mu.Unlock()
	if lines > 50 {
		t.Errorf("error sink saw %d lines, want at most 50", lines)
	}
	if got := failure.joined(); !strings.Contains(got, "REASON") {
		t.Errorf("the capped tail dropped the actual error: %q", got)
	}
	if got := failure.joined(); strings.Contains(got, "line-1\n") {
		t.Errorf("the capped tail kept the oldest output: %q", got)
	}
}

// OperatorCommand is the seam the readiness command shares with the hook, so its
// two guarantees are worth pinning at this level rather than only through the two
// call sites: the script runs in the worktree it was given, and cancelling the
// context takes the whole process group with it.
func TestOperatorCommandRunsInTheGivenDirectory(t *testing.T) {
	dir := t.TempDir()
	cmd := hook.OperatorCommand(context.Background(), "echo ran > marker", dir)
	if err := cmd.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "marker")); err != nil {
		t.Fatalf("script did not run with cwd %q: %v", dir, err)
	}
}

func TestOperatorCommandCancellationKillsTheProcessGroup(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "grandchild-survived")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	cmd := hook.OperatorCommand(ctx, "sh -c 'sleep 5; touch "+marker+"' & wait", dir)
	start := time.Now()
	if err := cmd.Run(); err == nil {
		t.Fatal("Run returned nil for a script whose context expired")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Run took %s, want the cancellation to cut it short", elapsed)
	}

	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("the script's grandchild outlived the cancellation: the process group was not killed")
	}
}
