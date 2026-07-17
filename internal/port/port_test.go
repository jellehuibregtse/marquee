//go:build darwin || linux

package port

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain lets a test re-exec this binary as a bare TCP listener. Spawned in
// its own session (Setsid), such a listener escapes a process-group kill — the
// exact remnant a daemonizing process manager leaves behind — so the reap tests
// exercise the real kill-by-port path.
func TestMain(m *testing.M) {
	if p := os.Getenv("PORT_TEST_LISTEN"); p != "" {
		ln, err := net.Listen("tcp", "127.0.0.1:"+p)
		if err != nil {
			os.Exit(1)
		}
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}
	os.Exit(m.Run())
}

func freePortForTest(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick free port: %v", err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

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

// TestFreeFastWhenFree: a free port needs no reaping and returns promptly
// without touching lsof or killing anything.
func TestFreeFastWhenFree(t *testing.T) {
	p := freePortForTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	if err := Free(ctx, p, nil); err != nil {
		t.Fatalf("Free on a free port: %v", err)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Errorf("Free on a free port took %v, want it to return promptly", d)
	}
}

// TestFreeReapsEscapedListener: a listener in its own session (so a
// process-group kill would miss it) squatting the internal port is found and
// reaped, the port ends up free, and exactly one non-secret reap line is logged.
func TestFreeReapsEscapedListener(t *testing.T) {
	p := freePortForTest(t)
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "PORT_TEST_LISTEN="+strconv.Itoa(p))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start escaped listener: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	addr := fmt.Sprintf("127.0.0.1:%d", p)
	waitFor(t, 2*time.Second, "escaped listener up", func() bool {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	})

	var logged []string
	logf := func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := Free(ctx, p, logf); err != nil {
		t.Fatalf("Free: %v", err)
	}
	if held(p) {
		t.Fatal("internal port still held after Free reaped its listener")
	}
	if len(logged) != 1 {
		t.Fatalf("reap logged %d lines, want 1: %v", len(logged), logged)
	}
	if !strings.Contains(logged[0], strconv.Itoa(p)) || !strings.Contains(logged[0], "reaped") {
		t.Errorf("reap log = %q, want it to name the internal port %d and say it was reaped", logged[0], p)
	}
	if strings.Contains(logged[0], "PORT_TEST_LISTEN") || strings.Contains(logged[0], os.Args[0]) {
		t.Errorf("reap log leaked a command line: %q", logged[0])
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
