//go:build darwin || linux

package runner

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", timeout, what)
}

func stopHard(t *testing.T, r *Runner) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.Stop(ctx, nil); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestStartSetsOwnProcessGroup(t *testing.T) {
	r := New([]string{"sleep", "30"}, nil, "")
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer stopHard(t, r)

	pid := r.cmd.Process.Pid
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatalf("Getpgid: %v", err)
	}
	if pgid != pid {
		t.Errorf("child pgid = %d, want %d (child should lead its own group)", pgid, pid)
	}
	if pgid == syscall.Getpgrp() {
		t.Errorf("child pgid %d equals parent pgid (child should not share the parent group)", pgid)
	}
	if got := r.Status().State; got != StateRunning {
		t.Errorf("state = %q, want %q", got, StateRunning)
	}
}

func TestEnvReachesChild(t *testing.T) {
	out := filepath.Join(t.TempDir(), "env.txt")
	r := New(
		[]string{"sh", "-c", `printf '%s' "$MARQUEE_TEST" > "$OUT"`},
		[]string{"MARQUEE_TEST=hello", "OUT=" + out},
		"",
	)
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, 2*time.Second, "child exit", func() bool {
		return r.Status().State == StateExited
	})
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read %s: %v", out, err)
	}
	if string(got) != "hello" {
		t.Errorf("child saw MARQUEE_TEST=%q, want %q", got, "hello")
	}
}

func TestStopGraceful(t *testing.T) {
	r := New([]string{"sleep", "30"}, nil, "")
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := r.Stop(ctx, nil); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("graceful stop took %v, want prompt exit on SIGTERM", elapsed)
	}
	if got := r.Status().State; got != StateExited {
		t.Errorf("state = %q, want %q", got, StateExited)
	}
}

func TestStopEscalatesToSIGKILL(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	r := New(
		[]string{"sh", "-c", `trap "" TERM; : > "$READY"; while :; do sleep 0.05; done`},
		[]string{"READY=" + ready},
		"",
	)
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, 2*time.Second, "trap installed", func() bool {
		_, err := os.Stat(ready)
		return err == nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := r.Stop(ctx, nil); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 300*time.Millisecond {
		t.Errorf("Stop returned after %v, before the graceful window expired", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Errorf("Stop took %v, SIGKILL escalation should be immediate after the window", elapsed)
	}
	st := r.Status()
	if st.State != StateExited {
		t.Fatalf("state = %q, want %q", st.State, StateExited)
	}
	if st.Err == nil {
		t.Errorf("exit error = nil, want the SIGKILL exit error")
	}
}

func TestStopKillsGrandchildren(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	r := New(
		[]string{"sh", "-c", `sleep 30 & echo $! > "$PIDFILE"; wait`},
		[]string{"PIDFILE=" + pidFile},
		"",
	)
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	var grandchild int
	waitFor(t, 2*time.Second, "grandchild pid file", func() bool {
		b, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil || pid <= 0 {
			return false
		}
		grandchild = pid
		return true
	})

	stopHard(t, r)

	waitFor(t, 2*time.Second, "grandchild to die", func() bool {
		return errors.Is(syscall.Kill(grandchild, 0), syscall.ESRCH)
	})
}

func TestRestartHonorsNewDir(t *testing.T) {
	dir1 := mustEvalSymlinks(t, t.TempDir())
	dir2 := mustEvalSymlinks(t, t.TempDir())
	out := filepath.Join(t.TempDir(), "cwd.txt")
	r := New(
		[]string{"sh", "-c", `pwd >> "$OUT"; sleep 30`},
		[]string{"OUT=" + out},
		dir1,
	)
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer stopHard(t, r)

	waitFor(t, 2*time.Second, "first cwd line", func() bool {
		return len(outLines(t, out)) >= 1
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.Restart(ctx, dir2); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	waitFor(t, 2*time.Second, "second cwd line", func() bool {
		return len(outLines(t, out)) >= 2
	})

	lines := outLines(t, out)
	if got := mustEvalSymlinks(t, lines[0]); got != dir1 {
		t.Errorf("first run cwd = %q, want %q", got, dir1)
	}
	if got := mustEvalSymlinks(t, lines[1]); got != dir2 {
		t.Errorf("restarted run cwd = %q, want %q", got, dir2)
	}
	if got := r.Status().State; got != StateRunning {
		t.Errorf("state after restart = %q, want %q", got, StateRunning)
	}
}

func TestWaitTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := WaitTCP(ctx, ln.Addr().String(), 20*time.Millisecond); err != nil {
		t.Errorf("WaitTCP against live listener: %v", err)
	}

	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	addr := dead.Addr().String()
	_ = dead.Close()

	ctx2, cancel2 := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel2()
	err = WaitTCP(ctx2, addr, 20*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("WaitTCP against closed port = %v, want context.DeadlineExceeded", err)
	}
}

func mustEvalSymlinks(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", path, err)
	}
	return resolved
}

func outLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read %s: %v", path, err)
	}
	trimmed := strings.TrimSpace(string(b))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}
