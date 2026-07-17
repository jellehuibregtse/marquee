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

func terminatedFired(r *Runner) bool {
	select {
	case <-r.Terminated():
		return true
	default:
		return false
	}
}

func assertNotTerminated(t *testing.T, r *Runner, within time.Duration) {
	t.Helper()
	select {
	case <-r.Terminated():
		t.Fatal("Terminated fired, want it to stay open (this exit is not a terminal, unmanaged one)")
	case <-time.After(within):
	}
}

// Wrapper-mode regression: a child that exits on its own (no Stop, no Restart,
// no managed window) must fire Terminated so cmd/marquee shuts down as before.
func TestTerminatedFiresOnUnmanagedExit(t *testing.T) {
	r := New([]string{"sh", "-c", "exit 3"}, nil, "", nil)
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-r.Terminated():
	case <-time.After(2 * time.Second):
		t.Fatal("Terminated never fired for a child that exited on its own")
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

// The transient stop during a healthy Restart must not fire the terminal-exit
// signal. Run under -race: the wait goroutine and Restart touch the same state.
func TestTerminatedNotFiredDuringRestart(t *testing.T) {
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
	if got := r.Status().State; got != StateRunning {
		t.Fatalf("state after restart = %q, want running", got)
	}
	assertNotTerminated(t, r, 200*time.Millisecond)
}

// A managed window (what the switcher opens for the whole switch) suppresses
// Terminated even when the child dies inside it, and — crucially — closing the
// window does not retroactively fire it. This is what keeps marquee alive when
// a switch's target and revert both fail to boot.
func TestManagedWindowSuppressesTerminatedForever(t *testing.T) {
	r := New([]string{"sleep", "30"}, nil, "", nil)
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	r.BeginManaged()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.Stop(ctx, nil); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if terminatedFired(r) {
		t.Fatal("Terminated fired for an exit inside a managed window")
	}
	r.EndManaged()
	assertNotTerminated(t, r, 200*time.Millisecond)
}

// After a successful restart the managed window is closed; a later natural death
// of the new child must fire Terminated, so marquee still shuts down when the
// dev server dies on its own after a switch (contract 4, post-switch).
func TestTerminatedFiresAfterRestartOnNaturalDeath(t *testing.T) {
	r := New([]string{"sleep", "30"}, nil, "", nil)
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.Restart(ctx, ""); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	// Kill the restarted child's process group out-of-band so it looks like a
	// natural, unmanaged death (no Stop/Restart involved).
	if err := r.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("Signal: %v", err)
	}
	select {
	case <-r.Terminated():
	case <-time.After(2 * time.Second):
		t.Fatal("Terminated never fired for a natural death after a restart")
	}
}
