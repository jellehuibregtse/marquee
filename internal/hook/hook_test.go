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

func TestRunUsesTheGivenWorktreeAsWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	r := hook.New(hook.Config{Command: "echo hooked > marker"})
	if err := r.Run(context.Background(), dir); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "marker")); err != nil {
		t.Fatalf("hook did not run with cwd %q: %v", dir, err)
	}
}

func TestRunReportsANonZeroExit(t *testing.T) {
	r := hook.New(hook.Config{Command: "exit 3"})
	err := r.Run(context.Background(), t.TempDir())
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
	if err := hook.New(hook.Config{}).Run(context.Background(), dir); err != nil {
		t.Fatalf("empty hook: %v", err)
	}
	var nilRunner *hook.Runner
	if err := nilRunner.Run(context.Background(), dir); err != nil {
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
// spawned: the hook leads its own process group and the whole group is killed on
// timeout. The grandchild writes a marker after its parent's deadline, so a
// surviving group would leave it behind.
func TestTimeoutKillsTheWholeHookProcessGroup(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "grandchild-survived")
	r := hook.New(hook.Config{Command: "sh -c 'sleep 5; touch " + marker + "' & wait", Timeout: 200 * time.Millisecond})

	start := time.Now()
	err := r.Run(context.Background(), dir)
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

func TestOutputIsStreamedToTheLoggerLineByLine(t *testing.T) {
	var rec recorder
	r := hook.New(hook.Config{Command: "echo first; echo second >&2; printf trailing", Logf: rec.logf})
	if err := r.Run(context.Background(), t.TempDir()); err != nil {
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
	err := hook.New(hook.Config{Command: "sleep 5"}).Run(ctx, t.TempDir())
	if err == nil {
		t.Fatal("Run returned nil for a hook cancelled mid-flight")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Run took %s, want the cancellation to cut it short", elapsed)
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
	if err := r.Run(context.Background(), t.TempDir()); err == nil {
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
	if err := r.Run(context.Background(), t.TempDir()); err != nil {
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
	if err := r.Run(context.Background(), t.TempDir()); err == nil {
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
