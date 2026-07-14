package gitinfo

import (
	"testing"
)

func TestPollerRepointSwitchesDirectory(t *testing.T) {
	a := tempDir(t)
	initRepo(t, a)
	b := tempDir(t)
	initRepo(t, b)
	gitCmd(t, b, "branch", "-m", "amber")

	p := Start(a, 10_000_000, nil) // 10ms
	defer p.Stop()
	if got := p.Snapshot().Branch; got != "trunk" {
		t.Fatalf("initial branch = %q, want trunk", got)
	}

	p.Repoint(b)
	if got := p.Snapshot().Branch; got != "amber" {
		t.Fatalf("after repoint branch = %q, want amber (repoint refreshes synchronously)", got)
	}
	if got := p.Snapshot().RepoRoot; got != b {
		t.Errorf("after repoint RepoRoot = %q, want %q", got, b)
	}
}
