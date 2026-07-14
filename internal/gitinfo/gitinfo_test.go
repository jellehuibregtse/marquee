package gitinfo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func initRepo(t *testing.T, dir string) {
	t.Helper()
	gitCmd(t, dir, "init", "-b", "trunk")
	gitCmd(t, dir, "config", "user.name", "Fixture Author")
	gitCmd(t, dir, "config", "user.email", "fixture@example.com")
	gitCmd(t, dir, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", ".")
	gitCmd(t, dir, "commit", "-m", "Add notes")
}

type logCounter struct {
	mu    sync.Mutex
	lines []string
}

func (l *logCounter) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, format)
}

func (l *logCounter) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.lines)
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestCollectCleanRepo(t *testing.T) {
	dir := tempDir(t)
	initRepo(t, dir)

	snap, err := collect(dir)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if snap.Branch != "trunk" {
		t.Errorf("Branch = %q, want %q", snap.Branch, "trunk")
	}
	if snap.Dirty {
		t.Error("Dirty = true, want false")
	}
	if snap.RepoRoot != dir {
		t.Errorf("RepoRoot = %q, want %q", snap.RepoRoot, dir)
	}
	want := CurrentWorktree{Path: dir, Slug: filepath.Base(dir), IsMain: true}
	if snap.Worktree != want {
		t.Errorf("Worktree = %+v, want %+v", snap.Worktree, want)
	}
	if len(snap.Worktrees) != 1 {
		t.Fatalf("Worktrees = %+v, want one entry", snap.Worktrees)
	}
	if snap.Worktrees[0].Branch != "trunk" || snap.Worktrees[0].Path != dir {
		t.Errorf("Worktrees[0] = %+v", snap.Worktrees[0])
	}
}

func TestCollectDirty(t *testing.T) {
	dir := tempDir(t)
	initRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	snap, err := collect(dir)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if !snap.Dirty {
		t.Error("Dirty = false after modifying a tracked file, want true")
	}
}

func TestCollectWorktrees(t *testing.T) {
	dir := tempDir(t)
	initRepo(t, dir)
	wtPath := filepath.Join(tempDir(t), "lantern")
	gitCmd(t, dir, "worktree", "add", "-b", "lantern", wtPath)

	snap, err := collect(dir)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(snap.Worktrees) != 2 {
		t.Fatalf("Worktrees = %+v, want two entries", snap.Worktrees)
	}
	main := snap.Worktrees[0]
	if main.Path != dir || main.Slug != filepath.Base(dir) || main.Branch != "trunk" {
		t.Errorf("main worktree = %+v", main)
	}
	linked := snap.Worktrees[1]
	if linked.Path != wtPath || linked.Slug != "lantern" || linked.Branch != "lantern" {
		t.Errorf("linked worktree = %+v", linked)
	}
	if !snap.Worktree.IsMain {
		t.Error("IsMain = false in the main worktree, want true")
	}

	fromLinked, err := collect(wtPath)
	if err != nil {
		t.Fatalf("collect in linked worktree: %v", err)
	}
	wantCurrent := CurrentWorktree{Path: wtPath, Slug: "lantern", IsMain: false}
	if fromLinked.Worktree != wantCurrent {
		t.Errorf("Worktree = %+v, want %+v", fromLinked.Worktree, wantCurrent)
	}
	if fromLinked.Branch != "lantern" {
		t.Errorf("Branch = %q, want %q", fromLinked.Branch, "lantern")
	}
	if fromLinked.RepoRoot != wtPath {
		t.Errorf("RepoRoot = %q, want %q", fromLinked.RepoRoot, wtPath)
	}
}

func TestNonGitDirServesZeroStateAndLogsOnce(t *testing.T) {
	logs := &logCounter{}
	p := Start(tempDir(t), 10*time.Millisecond, logs.logf)
	defer p.Stop()

	if snap := p.Snapshot(); snap.Branch != "" || snap.RepoRoot != "" || snap.Dirty || len(snap.Worktrees) != 0 {
		t.Errorf("Snapshot = %+v, want zero state", snap)
	}
	time.Sleep(100 * time.Millisecond)
	if got := logs.count(); got != 1 {
		t.Errorf("logged %d times for a persistent failure, want 1", got)
	}
}

func TestPollerRefreshesAfterChange(t *testing.T) {
	dir := tempDir(t)
	initRepo(t, dir)
	p := Start(dir, 10*time.Millisecond, nil)
	defer p.Stop()

	if p.Snapshot().Dirty {
		t.Fatal("initial snapshot Dirty = true, want false")
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "dirty flag to flip", func() bool { return p.Snapshot().Dirty })
}

func TestPollerServesStaleOnFailure(t *testing.T) {
	base := tempDir(t)
	dir := filepath.Join(base, "ember")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	initRepo(t, dir)

	logs := &logCounter{}
	p := Start(dir, 10*time.Millisecond, logs.logf)
	defer p.Stop()

	if got := p.Snapshot().Branch; got != "trunk" {
		t.Fatalf("Branch = %q before failure, want %q", got, "trunk")
	}
	// Rename instead of RemoveAll: removal is not atomic, so a poll racing a
	// partial removal fails differently than one after full removal, which
	// log-once correctly reports as two lines. Rename makes the directory
	// vanish atomically, so every failed poll yields the same error.
	if err := os.Rename(dir, filepath.Join(base, "gone")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "failure to be logged", func() bool { return logs.count() >= 1 })
	time.Sleep(100 * time.Millisecond)

	if got := p.Snapshot().Branch; got != "trunk" {
		t.Errorf("Branch = %q after failure, want stale %q", got, "trunk")
	}
	if got := logs.count(); got != 1 {
		t.Errorf("logged %d times for a persistent failure, want 1", got)
	}
}
