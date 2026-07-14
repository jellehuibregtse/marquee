// Package gitinfo collects git state for the status endpoint — current
// branch, dirty flag, worktree list, and repo root — by shelling out to git,
// polled and served from cache.
package gitinfo

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
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
