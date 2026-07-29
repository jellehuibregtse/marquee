package main

import (
	"strings"
	"testing"

	"github.com/jellehuibregtse/marquee/internal/gitinfo"
)

// The status payload the bar reads is filtered too, so the picker only offers
// worktrees a switch would accept. Everything else about the snapshot is left
// alone, including the current worktree when it is one of the excluded ones.
func TestFilterWorktreesNarrowsTheStatusPayload(t *testing.T) {
	filter, err := gitinfo.NewWorktreeFilter([]string{"/code/wt/*"})
	if err != nil {
		t.Fatalf("NewWorktreeFilter: %v", err)
	}
	snap := gitinfo.Snapshot{
		Branch:   "feature",
		Worktree: gitinfo.CurrentWorktree{Path: "/elsewhere/demo", Slug: "demo"},
		Worktrees: []gitinfo.Worktree{
			{Slug: "repo", Path: "/code/repo"},
			{Slug: "feature", Path: "/code/wt/feature"},
			{Slug: "demo", Path: "/elsewhere/demo"},
		},
	}

	got := filterWorktrees(snap, filter)
	if len(got.Worktrees) != 2 || got.Worktrees[0].Slug != "repo" || got.Worktrees[1].Slug != "feature" {
		t.Errorf("worktrees = %v, want the main worktree and feature", got.Worktrees)
	}
	if got.Branch != "feature" || got.Worktree.Slug != "demo" {
		t.Errorf("filtering changed the rest of the snapshot: %+v", got)
	}
}

func TestSwitchTargetLines(t *testing.T) {
	all := []gitinfo.Worktree{
		{Slug: "repo", Path: "/code/repo"},
		{Slug: "feature", Path: "/code/wt/feature"},
		{Slug: "demo", Path: "/elsewhere/demo"},
	}

	info, warn := switchTargetLines(all, all[:2])
	if want := "switch targets (2 of 3 worktrees): repo, feature"; info != want {
		t.Errorf("info = %q, want %q", info, want)
	}
	if warn != "" {
		t.Errorf("warn = %q, want none when a glob matched something", warn)
	}

	// Only main surviving means the bar hides its switcher, which reads as a broken
	// feature, so it has to warn and it has to name the path traps behind it.
	info, warn = switchTargetLines(all, all[:1])
	if want := "switch targets (1 of 3 worktrees): repo"; info != want {
		t.Errorf("info = %q, want %q", info, want)
	}
	for _, trap := range []string{"resolved", "crosses", "case-sensitive"} {
		if !strings.Contains(warn, trap) {
			t.Errorf("warning does not mention %q: %q", trap, warn)
		}
	}
}

func TestLoopbackHost(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.2", true},
		{"::1", true},
		{"localhost", true},
		{"LocalHost", true},
		{"app.localhost", true},
		{"", false},
		{"0.0.0.0", false},
		{"::", false},
		{"192.168.1.5", false},
		{"10.0.0.1", false},
		{"example.com", false},
		{"lvh.me", false},
	}
	for _, tc := range tests {
		if got := loopbackHost(tc.host); got != tc.want {
			t.Errorf("loopbackHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}
