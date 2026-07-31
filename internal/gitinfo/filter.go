package gitinfo

import (
	"fmt"
	"path/filepath"
)

// WorktreeFilter narrows a repo's worktree set down to the ones an operator
// wants offered as switch targets. A working machine can easily have dozens of
// worktrees (throwaway demo checkouts, agent scratch trees) that are noise in
// the bar's picker and a mis-click away from a pointless switch.
//
// The zero value keeps every worktree, so a run with no patterns behaves exactly
// as before.
type WorktreeFilter struct {
	globs []string
}

// NewWorktreeFilter validates globs and returns the filter they describe. Each
// pattern is matched against a worktree's absolute path with filepath.Match, so
// "*" does not cross a path separator: a directory holding worktrees needs a
// trailing "/*" to match the trees inside it.
func NewWorktreeFilter(globs []string) (WorktreeFilter, error) {
	for _, glob := range globs {
		if glob == "" {
			return WorktreeFilter{}, fmt.Errorf("empty worktree glob")
		}
		// filepath.Match reports a malformed pattern (an unclosed character class)
		// independently of what it is matched against, so any subject validates it.
		if _, err := filepath.Match(glob, string(filepath.Separator)); err != nil {
			return WorktreeFilter{}, fmt.Errorf("invalid worktree glob %q: %w", glob, err)
		}
	}
	return WorktreeFilter{globs: append([]string(nil), globs...)}, nil
}

// Apply returns the worktrees that are switch targets, in the order it was given
// them. With no patterns every worktree stays a target. The main worktree, which
// is always first, is kept whatever the patterns say: switching back to main is
// the escape hatch out of a dirty or broken worktree (the one switch that skips
// the dirty-confirm gate), so no pattern may take it away.
func (f WorktreeFilter) Apply(worktrees []Worktree) []Worktree {
	if len(f.globs) == 0 {
		return worktrees
	}
	kept := make([]Worktree, 0, len(worktrees))
	for i, wt := range worktrees {
		if i == 0 || f.matches(wt.Path) {
			kept = append(kept, wt)
		}
	}
	return kept
}

func (f WorktreeFilter) matches(path string) bool {
	for _, glob := range f.globs {
		if ok, err := filepath.Match(glob, path); err == nil && ok {
			return true
		}
	}
	return false
}
