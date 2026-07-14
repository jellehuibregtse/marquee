package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jellehuibregtse/marquee/internal/runner"
)

func TestPidfilePathVariesByListenAddr(t *testing.T) {
	a, err := pidfilePath("127.0.0.1:3000")
	if err != nil {
		t.Fatalf("pidfilePath: %v", err)
	}
	b, err := pidfilePath("127.0.0.1:3001")
	if err != nil {
		t.Fatalf("pidfilePath: %v", err)
	}
	if a == b {
		t.Errorf("pidfile path %q identical for different listen addresses", a)
	}
	if dir := filepath.Base(filepath.Dir(a)); dir != "marquee" {
		t.Errorf("pidfile dir = %q, want %q", dir, "marquee")
	}
	if !strings.HasSuffix(a, ".pid") {
		t.Errorf("pidfile path %q does not end in .pid", a)
	}
}

func TestPidfileWrittenOnSpawnRemovedOnStop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "child.pid")
	child := runner.New([]string{"sleep", "30"}, nil, "")
	if err := child.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = child.Stop(ctx, nil)
	}()

	pgid := child.PGID()
	if pgid <= 0 {
		t.Fatalf("PGID = %d, want a positive process-group id", pgid)
	}
	if err := syscall.Kill(-pgid, 0); err != nil {
		t.Fatalf("process group %d not alive after spawn: %v", pgid, err)
	}
	if err := writePidfile(path, pgid); err != nil {
		t.Fatalf("writePidfile: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pidfile: %v", err)
	}
	if got := strings.TrimSpace(string(b)); got != strconv.Itoa(pgid) {
		t.Errorf("pidfile contains %q, want %d", got, pgid)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := child.Stop(ctx, nil); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	removePidfile(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("pidfile still present after clean stop: %v", err)
	}
}

func TestWarnStaleChildDeadGroupCleansUpSilently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stale.pid")
	cmd := exec.Command("true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	pgid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for groupAlive(pgid) {
		if time.Now().After(deadline) {
			t.Fatalf("process group %d still alive after exit", pgid)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := writePidfile(path, pgid); err != nil {
		t.Fatalf("writePidfile: %v", err)
	}

	var buf bytes.Buffer
	warnStaleChild(path, &buf)

	if buf.Len() != 0 {
		t.Errorf("dead group should clean up silently, got warning %q", buf.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("stale pidfile with dead group not removed: %v", err)
	}
}

func TestWarnStaleChildGarbageCleansUpSilently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "garbage.pid")
	if err := os.WriteFile(path, []byte("not-a-pgid\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var buf bytes.Buffer
	warnStaleChild(path, &buf)

	if buf.Len() != 0 {
		t.Errorf("garbage pidfile should clean up silently, got %q", buf.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("garbage pidfile not removed: %v", err)
	}
}

func TestWarnStaleChildPgidOneCleansUpSilently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "one.pid")
	if err := os.WriteFile(path, []byte("1\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var buf bytes.Buffer
	warnStaleChild(path, &buf)

	if buf.Len() != 0 {
		t.Errorf("pgid 1 must never warn (suggesting kill -TERM -1 is catastrophic), got %q", buf.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("pidfile with pgid 1 not removed: %v", err)
	}
}

func TestWarnStaleChildAliveGroupWarnsWithoutKilling(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghost.pid")
	ghost := exec.Command("sleep", "30")
	ghost.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := ghost.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	pgid := ghost.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		_ = ghost.Wait()
	})
	if err := writePidfile(path, pgid); err != nil {
		t.Fatalf("writePidfile: %v", err)
	}

	var buf bytes.Buffer
	warnStaleChild(path, &buf)

	out := buf.String()
	if !strings.Contains(out, strconv.Itoa(pgid)) {
		t.Errorf("warning %q does not name pgid %d", out, pgid)
	}
	if !strings.Contains(out, path) {
		t.Errorf("warning %q does not name pidfile %s", out, path)
	}
	if want := fmt.Sprintf("kill -TERM -%d", pgid); !strings.Contains(out, want) {
		t.Errorf("warning %q does not suggest %q", out, want)
	}
	if err := syscall.Kill(-pgid, 0); err != nil {
		t.Errorf("ghost group %d no longer alive — warnStaleChild must never kill: %v", pgid, err)
	}
}
