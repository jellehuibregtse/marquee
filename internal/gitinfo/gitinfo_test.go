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

// fixtureIdentity is the author and committer of every fixture commit. It rides
// in the environment because a `git config` write lands in whatever repository
// the directory turns out to belong to, and one that resolved somewhere real
// once renamed this repository's own author for eleven commits.
var fixtureIdentity = []string{
	"GIT_AUTHOR_NAME=Fixture Author",
	"GIT_AUTHOR_EMAIL=fixture@example.com",
	"GIT_COMMITTER_NAME=Fixture Author",
	"GIT_COMMITTER_EMAIL=fixture@example.com",
}

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), fixtureIdentity...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// gitCmdAt runs git with a fixed committer date, so a fixture can order branches
// by recency without depending on elapsed time — git records committerdate at
// whole-second resolution, which commits made in one test run would share.
func gitCmdAt(t *testing.T, dir, date string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), fixtureIdentity...)
	cmd.Env = append(cmd.Env, "GIT_COMMITTER_DATE="+date, "GIT_AUTHOR_DATE="+date)
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

func commitAt(t *testing.T, dir, date, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmdAt(t, dir, date, "commit", "-am", "Update notes")
}

func assertSlugs(t *testing.T, worktrees []Worktree, want ...string) {
	t.Helper()
	got := make([]string, 0, len(worktrees))
	for _, wt := range worktrees {
		got = append(got, wt.Slug)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("worktree order = %v, want %v", got, want)
	}
}

func TestCollectOrdersWorktreesByBranchRecency(t *testing.T) {
	dir := tempDir(t)
	initRepo(t, dir)
	base := tempDir(t)
	addWorktree := func(slug string, flags ...string) string {
		path := filepath.Join(base, slug)
		gitCmd(t, dir, append(append([]string{"worktree", "add"}, flags...), path)...)
		return path
	}
	// The add order is deliberately not the recency order, and the detached tree
	// sits in the middle of it, so passing means the ordering ran rather than
	// git's own list order happening to match.
	beacon := addWorktree("beacon", "-b", "beacon")
	addWorktree("adrift", "--detach")
	sundial := addWorktree("sundial", "-b", "sundial")
	lantern := addWorktree("lantern", "-b", "lantern")

	commitAt(t, beacon, "2026-02-01T09:00:00+01:00", "beacon\n")
	commitAt(t, sundial, "2026-03-01T09:00:00+01:00", "sundial\n")
	commitAt(t, lantern, "2026-04-01T09:00:00+01:00", "lantern\n")
	// trunk is the least recently committed branch, so it only stays first if the
	// main worktree is pinned there.
	commitAt(t, dir, "2026-01-01T09:00:00+01:00", "trunk\n")

	snap, err := collect(dir)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	assertSlugs(t, snap.Worktrees, filepath.Base(dir), "lantern", "sundial", "beacon", "adrift")
	if !snap.Worktree.IsMain {
		t.Error("IsMain = false in the main worktree after reordering, want true")
	}
}

func TestOrderByRecencyPinsMainAndSinksUndatedBranches(t *testing.T) {
	worktrees := []Worktree{
		{Slug: "harbour", Branch: "trunk"},
		{Slug: "adrift"},
		{Slug: "beacon", Branch: "beacon"},
		{Slug: "kiln", Branch: "kiln"},
		{Slug: "lantern", Branch: "lantern"},
		{Slug: "cinder"},
	}
	// kiln has a branch but no entry, which is what a branch with no commits
	// looks like to for-each-ref; adrift and cinder are detached heads.
	refs := "lantern\nbeacon\ntrunk\n"

	assertSlugs(t, orderByRecency(worktrees, refs), "harbour", "lantern", "beacon", "adrift", "kiln", "cinder")
	assertSlugs(t, worktrees, "harbour", "adrift", "beacon", "kiln", "lantern", "cinder")
}

func TestOrderByRecencyWithoutDatedBranchesKeepsGitOrder(t *testing.T) {
	worktrees := []Worktree{
		{Slug: "harbour", Branch: "trunk"},
		{Slug: "beacon", Branch: "beacon"},
		{Slug: "adrift"},
	}

	assertSlugs(t, orderByRecency(worktrees, ""), "harbour", "beacon", "adrift")
	assertSlugs(t, orderByRecency(nil, ""))
	assertSlugs(t, orderByRecency([]Worktree{{Slug: "harbour"}}, "\n\n"), "harbour")
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

func TestRunGitDeclinesOptionalLocks(t *testing.T) {
	dir := tempDir(t)
	bin := tempDir(t)
	argsFile := filepath.Join(dir, "args")
	stub := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	if _, err := runGit(dir, "status", "--porcelain"); err != nil {
		t.Fatalf("runGit: %v", err)
	}

	recorded, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	// The flag is a top-level git option, so it is only honored ahead of the
	// subcommand.
	want := "--no-optional-locks\nstatus\n--porcelain\n"
	if string(recorded) != want {
		t.Errorf("git got args %q, want %q", recorded, want)
	}
}

func TestPollerCollectsOnlyForReaders(t *testing.T) {
	dir := tempDir(t)
	initRepo(t, dir)
	p := Start(dir, 10*time.Millisecond, nil)
	defer p.Stop()

	// Start's own collect is the only one anybody asked for.
	if got := p.collectCount(); got != 1 {
		t.Fatalf("collects after Start = %d, want 1", got)
	}
	time.Sleep(150 * time.Millisecond)
	if got := p.collectCount(); got != 1 {
		t.Errorf("collects after ~15 unread ticks = %d, want 1", got)
	}

	p.Snapshot()
	waitFor(t, "the read to arm one collect", func() bool { return p.collectCount() == 2 })
	time.Sleep(150 * time.Millisecond)
	if got := p.collectCount(); got != 2 {
		t.Errorf("collects after a single read = %d, want 2 (one read arms one collect)", got)
	}
}
