package gitinfo

import (
	"strings"
	"testing"
)

func worktreeSet() []Worktree {
	return []Worktree{
		{Slug: "main", Path: "/code/repo"},
		{Slug: "feature", Path: "/code/worktrees/feature"},
		{Slug: "demo", Path: "/tmp/scratch/demo"},
		{Slug: "agent", Path: "/code/worktrees/nested/agent"},
	}
}

func slugs(worktrees []Worktree) string {
	got := make([]string, 0, len(worktrees))
	for _, wt := range worktrees {
		got = append(got, wt.Slug)
	}
	return strings.Join(got, ",")
}

func TestWorktreeFilterZeroValueKeepsEverything(t *testing.T) {
	var filter WorktreeFilter
	if got, want := slugs(filter.Apply(worktreeSet())), "main,feature,demo,agent"; got != want {
		t.Errorf("Apply = %q, want %q", got, want)
	}
}

func TestWorktreeFilterNoGlobsKeepsEverything(t *testing.T) {
	filter, err := NewWorktreeFilter(nil)
	if err != nil {
		t.Fatalf("NewWorktreeFilter: %v", err)
	}
	if got, want := slugs(filter.Apply(worktreeSet())), "main,feature,demo,agent"; got != want {
		t.Errorf("Apply = %q, want %q", got, want)
	}
}

func TestWorktreeFilterKeepsMatchesAndMain(t *testing.T) {
	// The glob matches only /code/worktrees/feature: "*" does not cross a
	// separator, so the nested agent tree is out, and so is the demo checkout. The
	// main worktree stays regardless of the glob.
	filter, err := NewWorktreeFilter([]string{"/code/worktrees/*"})
	if err != nil {
		t.Fatalf("NewWorktreeFilter: %v", err)
	}
	if got, want := slugs(filter.Apply(worktreeSet())), "main,feature"; got != want {
		t.Errorf("Apply = %q, want %q", got, want)
	}
}

func TestWorktreeFilterGlobsAreAdditive(t *testing.T) {
	filter, err := NewWorktreeFilter([]string{"/code/worktrees/*", "/tmp/scratch/*"})
	if err != nil {
		t.Fatalf("NewWorktreeFilter: %v", err)
	}
	if got, want := slugs(filter.Apply(worktreeSet())), "main,feature,demo"; got != want {
		t.Errorf("Apply = %q, want %q", got, want)
	}
}

func TestWorktreeFilterKeepsMainWhenNothingMatches(t *testing.T) {
	filter, err := NewWorktreeFilter([]string{"/nowhere/*"})
	if err != nil {
		t.Fatalf("NewWorktreeFilter: %v", err)
	}
	if got, want := slugs(filter.Apply(worktreeSet())), "main"; got != want {
		t.Errorf("Apply = %q, want %q (switching back to main must never be filtered away)", got, want)
	}
}

func TestWorktreeFilterEmptyWorktreeSet(t *testing.T) {
	filter, err := NewWorktreeFilter([]string{"/code/*"})
	if err != nil {
		t.Fatalf("NewWorktreeFilter: %v", err)
	}
	if got := filter.Apply(nil); len(got) != 0 {
		t.Errorf("Apply(nil) = %v, want empty", got)
	}
}

func TestNewWorktreeFilterRejectsBadPatterns(t *testing.T) {
	for _, glob := range []string{"", "/code/[a-"} {
		if _, err := NewWorktreeFilter([]string{glob}); err == nil {
			t.Errorf("NewWorktreeFilter(%q) accepted a bad pattern", glob)
		}
	}
}
