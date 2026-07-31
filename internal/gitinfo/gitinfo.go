// Package gitinfo collects git state for the status endpoint — current
// branch, dirty flag, worktree list, and repo root — by shelling out to git,
// polled and served from cache.
package gitinfo

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const commandTimeout = 2 * time.Second

type Worktree struct {
	Slug   string `json:"slug"`
	Path   string `json:"path"`
	Branch string `json:"branch"`
}

type CurrentWorktree struct {
	Path   string `json:"path"`
	Slug   string `json:"slug"`
	IsMain bool   `json:"isMain"`
}

type Snapshot struct {
	Branch    string          `json:"branch"`
	Dirty     bool            `json:"dirty"`
	Worktree  CurrentWorktree `json:"worktree"`
	RepoRoot  string          `json:"repoRoot"`
	Worktrees []Worktree      `json:"worktrees"`
}

// Collect gathers a fresh git Snapshot for dir by shelling out to git. It is
// the same collection the poller caches, exposed so the worktree switcher can
// validate a switch request against the live worktree set — parsed from git's
// own `worktree list --porcelain` output — rather than a possibly-stale cache.
func Collect(dir string) (Snapshot, error) { return collect(dir) }

func collect(dir string) (Snapshot, error) {
	branch, err := runGit(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return Snapshot{}, err
	}
	status, err := runGit(dir, "status", "--porcelain")
	if err != nil {
		return Snapshot{}, err
	}
	root, err := runGit(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return Snapshot{}, err
	}
	worktreeList, err := runGit(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return Snapshot{}, err
	}
	worktrees := parseWorktrees(worktreeList)
	mainPath := ""
	if len(worktrees) > 0 {
		mainPath = worktrees[0].Path
	}
	// Recency is a nicety layered on the snapshot, so a for-each-ref failure
	// leaves git's own order in place instead of failing the collect and freezing
	// the poller on a stale branch and dirty flag.
	if refs, refErr := runGit(dir, "for-each-ref", "--sort=-committerdate", "--format=%(refname:short)", "refs/heads"); refErr == nil {
		worktrees = orderByRecency(worktrees, refs)
	}
	return Snapshot{
		Branch: branch,
		Dirty:  status != "",
		Worktree: CurrentWorktree{
			Path:   root,
			Slug:   filepath.Base(root),
			IsMain: root == mainPath,
		},
		RepoRoot:  root,
		Worktrees: worktrees,
	}, nil
}

func runGit(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	// #nosec G204 -- args are fixed git subcommands chosen internally, never derived from HTTP or user input.
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), detail)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func parseWorktrees(out string) []Worktree {
	var worktrees []Worktree
	for _, block := range strings.Split(strings.TrimSpace(out), "\n\n") {
		var wt Worktree
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				wt.Path = strings.TrimPrefix(line, "worktree ")
			case strings.HasPrefix(line, "branch "):
				wt.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
			}
		}
		if wt.Path == "" {
			continue
		}
		wt.Slug = filepath.Base(wt.Path)
		worktrees = append(worktrees, wt)
	}
	return worktrees
}

// orderByRecency reorders a worktree list so the trees someone is actually
// working in are the ones at the top of the bar's menu: the main worktree keeps
// git's leading position, and the rest follow their branch's last commit,
// newest first. A worktree with no dated branch — a detached HEAD, or a branch
// whose ref for-each-ref did not list — sorts last, so it has a defined place
// instead of wherever git happened to emit it.
//
// The main worktree stays pinned because being first is load-bearing, not
// cosmetic: collect reads worktrees[0] to decide IsMain, and WorktreeFilter
// keeps index 0 whatever the --worktree-glob patterns say, so switching back to
// main survives as the escape hatch. Recency would also be the wrong signal for
// it — main is the tree you return to, not the one you just committed on.
//
// refs is `for-each-ref --sort=-committerdate` output, one short refname per
// line, most recent first.
func orderByRecency(worktrees []Worktree, refs string) []Worktree {
	rank := make(map[string]int)
	for i, line := range strings.Split(refs, "\n") {
		if name := strings.TrimSpace(line); name != "" {
			rank[name] = i
		}
	}
	ordered := append([]Worktree(nil), worktrees...)
	rest := ordered
	if len(rest) > 0 {
		rest = rest[1:]
	}
	// A stable sort keeps git's order between worktrees that tie, which is every
	// undated one and any pair sharing a branch.
	sort.SliceStable(rest, func(i, j int) bool {
		return recencyRank(rest[i], rank) < recencyRank(rest[j], rank)
	})
	return ordered
}

func recencyRank(wt Worktree, rank map[string]int) int {
	if wt.Branch == "" {
		return math.MaxInt
	}
	if i, ok := rank[wt.Branch]; ok {
		return i
	}
	return math.MaxInt
}
