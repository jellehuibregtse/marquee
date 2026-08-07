package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionPathVariesByListenAddress(t *testing.T) {
	a, err := sessionPath("127.0.0.1:3000")
	if err != nil {
		t.Fatalf("sessionPath: %v", err)
	}
	b, err := sessionPath("127.0.0.1:3001")
	if err != nil {
		t.Fatalf("sessionPath: %v", err)
	}
	if a == b {
		t.Errorf("session path %q identical for different listen addresses", a)
	}
	if !strings.HasSuffix(a, ".json") {
		t.Errorf("session path %q does not end in .json", a)
	}
	if dir := filepath.Base(filepath.Dir(a)); dir != "marquee" {
		t.Errorf("session dir = %q, want %q", dir, "marquee")
	}
}

// TestSessionPathAndPidfilePathShareADirectory holds the two artifacts of one
// run together: the shutdown path removes both, and liveSessions globs only the
// session half out of that directory.
func TestSessionPathAndPidfilePathShareADirectory(t *testing.T) {
	s, err := sessionPath("127.0.0.1:3000")
	if err != nil {
		t.Fatalf("sessionPath: %v", err)
	}
	p, err := pidfilePath("127.0.0.1:3000")
	if err != nil {
		t.Fatalf("pidfilePath: %v", err)
	}
	if filepath.Dir(s) != filepath.Dir(p) {
		t.Errorf("session dir %q != pidfile dir %q", filepath.Dir(s), filepath.Dir(p))
	}
}

// TestWriteSessionIsUnreadableByOthers is the permission guarantee the token on
// disk rests on: the file is owner-only inside an owner-only directory, so the
// argument that it grants a *same-user* attacker nothing new is not quietly
// extended to every user on the machine.
func TestWriteSessionIsUnreadableByOthers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "marquee")
	path := filepath.Join(dir, "session.json")
	if err := writeSession(path, session{Listen: "127.0.0.1:3000", Token: "deadbeef", PID: os.Getpid()}); err != nil {
		t.Fatalf("writeSession: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("session file mode = %o, want 600", perm)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("session dir mode = %o, want 700", perm)
	}
}

func TestReadSessionRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	want := session{Listen: "127.0.0.1:3000", Token: "deadbeef", PID: 4242}
	if err := writeSession(path, want); err != nil {
		t.Fatalf("writeSession: %v", err)
	}
	got, err := readSession(path)
	if err != nil {
		t.Fatalf("readSession: %v", err)
	}
	if got != want {
		t.Errorf("readSession = %+v, want %+v", got, want)
	}
}

func TestReadSessionRejectsAnEmptyListenOrPid(t *testing.T) {
	for name, body := range map[string]string{
		"garbage":    "not json at all",
		"no listen":  `{"token":"deadbeef","pid":4242}`,
		"no pid":     `{"listen":"127.0.0.1:3000","token":"deadbeef"}`,
		"zero pid":   `{"listen":"127.0.0.1:3000","token":"deadbeef","pid":0}`,
		"empty file": ``,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.json")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			if _, err := readSession(path); err == nil {
				t.Errorf("readSession(%q) = nil error, want a rejection", body)
			}
		})
	}
}

// TestLiveSessionsDropsAndDeletesDeadOnes is the stale-token guarantee. A
// marquee killed with SIGKILL leaves a file whose token authenticates nothing,
// because the token is minted per process; the client must never dial that
// address on its word, and must clean up on the way past.
func TestLiveSessionsDropsAndDeletesDeadOnes(t *testing.T) {
	dir := t.TempDir()
	alive := filepath.Join(dir, "alive.json")
	if err := writeSession(alive, session{Listen: "127.0.0.1:3000", Token: "a", PID: os.Getpid()}); err != nil {
		t.Fatalf("writeSession: %v", err)
	}
	dead := filepath.Join(dir, "dead.json")
	if err := writeSession(dead, session{Listen: "127.0.0.1:3001", Token: "b", PID: deadPID(t)}); err != nil {
		t.Fatalf("writeSession: %v", err)
	}
	garbage := filepath.Join(dir, "garbage.json")
	if err := os.WriteFile(garbage, []byte("{"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	live, err := liveSessions(dir)
	if err != nil {
		t.Fatalf("liveSessions: %v", err)
	}
	if len(live) != 1 || live[0].Listen != "127.0.0.1:3000" {
		t.Fatalf("liveSessions = %+v, want only the live 127.0.0.1:3000", live)
	}
	for _, gone := range []string{dead, garbage} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s still exists, want it cleaned up", filepath.Base(gone))
		}
	}
	if _, err := os.Stat(alive); err != nil {
		t.Errorf("live session file was removed: %v", err)
	}
}

func TestLiveSessionsIgnoresTheSiblingPidfile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "abc.pid"), []byte("4242\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	live, err := liveSessions(dir)
	if err != nil {
		t.Fatalf("liveSessions: %v", err)
	}
	if len(live) != 0 {
		t.Errorf("liveSessions = %+v, want none", live)
	}
	if _, err := os.Stat(filepath.Join(dir, "abc.pid")); err != nil {
		t.Errorf("pidfile was removed by the session sweep: %v", err)
	}
}

// TestProcessAliveRefusesPidOneAndBelow mirrors the pidfile guard: 0 and
// negative pids mean "every process" or "the process group" to kill(2), and 1
// is init. None of them is ever a marquee.
func TestProcessAliveRefusesPidOneAndBelow(t *testing.T) {
	for _, pid := range []int{-1, 0, 1} {
		if processAlive(pid) {
			t.Errorf("processAlive(%d) = true, want false", pid)
		}
	}
	if !processAlive(os.Getpid()) {
		t.Error("processAlive(own pid) = false, want true")
	}
}

// deadPID finds a pid that is certainly not running, by starting a process and
// reaping it. Picking a large number instead would be a flake waiting for the
// machine to wrap its pid counter around onto it.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	return pid
}
