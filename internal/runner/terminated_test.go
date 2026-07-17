//go:build darwin || linux

package runner

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func exitReported(r *Runner) bool {
	select {
	case <-r.Exits():
		return true
	default:
		return false
	}
}

func assertNoExitReported(t *testing.T, r *Runner, within time.Duration) {
	t.Helper()
	select {
	case <-r.Exits():
		t.Fatal("an exit was reported on Exits, want none (this exit is one the runner caused)")
	case <-time.After(within):
	}
}

// Wrapper-mode regression: a child that exits on its own (no Stop, no Restart)
// is an unexpected termination and must be reported on Exits, so the switch
// orchestrator can forward it and cmd/marquee shuts down.
func TestExitReportedOnUnexpectedExit(t *testing.T) {
	r := New([]string{"sh", "-c", "exit 3"}, nil, "", nil)
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-r.Exits():
	case <-time.After(2 * time.Second):
		t.Fatal("Exits never fired for a child that exited on its own")
	}
	st := r.Status()
	if st.State != StateExited {
		t.Errorf("state = %q, want %q", st.State, StateExited)
	}
	var exit *exec.ExitError
	if !errors.As(st.Err, &exit) || exit.ExitCode() != 3 {
		t.Errorf("exit error = %v, want exit status 3", st.Err)
	}
}

// A Stop is an exit the runner caused: it must not be reported on Exits.
func TestExitNotReportedOnStop(t *testing.T) {
	r := New([]string{"sleep", "30"}, nil, "", nil)
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.Stop(ctx, nil); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if r.Alive() {
		t.Fatal("child still alive after Stop")
	}
	assertNoExitReported(t, r, 200*time.Millisecond)
}

// The transient stop during a healthy Restart is runner-caused and must not be
// reported. Run under -race: the wait goroutine and Restart touch the same
// state (the stopping flag).
func TestExitNotReportedDuringRestart(t *testing.T) {
	dir1 := mustEvalSymlinks(t, t.TempDir())
	dir2 := mustEvalSymlinks(t, t.TempDir())
	r := New([]string{"sleep", "30"}, nil, dir1, nil)
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer stopHard(t, r)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.Restart(ctx, dir2); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if !r.Alive() {
		t.Fatalf("child not alive after restart")
	}
	assertNoExitReported(t, r, 200*time.Millisecond)
}

// After a successful restart the new child is re-armed: a later natural death
// of it must be reported, so marquee still shuts down when the dev server dies
// on its own after a switch settles.
func TestExitReportedAfterRestartOnNaturalDeath(t *testing.T) {
	r := New([]string{"sleep", "30"}, nil, "", nil)
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.Restart(ctx, ""); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if exitReported(r) {
		t.Fatal("an exit was reported for the restart's transient stop")
	}
	// Kill the restarted child's process group out-of-band so it looks like a
	// natural, unmanaged death (no Stop/Restart involved).
	if err := r.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("Signal: %v", err)
	}
	select {
	case <-r.Exits():
	case <-time.After(2 * time.Second):
		t.Fatal("Exits never fired for a natural death after a restart")
	}
}
