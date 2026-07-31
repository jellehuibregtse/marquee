package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// conventionDir is the per-repo directory marquee reads its conventions out of.
// One directory rather than a dotfile per convention: a repo that needs a
// bootstrap script and a set of flags gets one place for both.
//
// It is resolved against the directory marquee was launched in, once, at startup.
// Resolving it per target worktree would be wrong for the switch: the worktrees
// people switch between are usually throwaway checkouts of the same repo that do
// not carry the file, and a hook that vanishes halfway through a session is worse
// than one that was never there.
const conventionDir = ".marquee"

// hookFile is the conventional switch hook: the script that makes a worktree
// runnable. An executable file here means the same thing as passing its path to
// --switch-hook.
const hookFile = "hook"

func hookPath(launchDir string) string { return filepath.Join(launchDir, conventionDir, hookFile) }

// resolveSwitchHook picks the switch-hook command for this run. An explicit
// --switch-hook always wins, including an explicit empty value, which is how an
// operator says "ignore the conventional hook" — the flag already reads empty as
// "no hook", so nothing new has to be learned to turn the convention off.
//
// notice is the line to log when the convention supplied the hook, since a script
// marquee found by itself has to announce that it is about to run. warning is the
// line to log for a .marquee/hook that exists but cannot be run.
func resolveSwitchHook(explicit string, explicitSet bool, launchDir string) (command, notice, warning string) {
	if explicitSet {
		return explicit, "", ""
	}
	command, warning = discoverHook(launchDir)
	if command == "" {
		return "", "", warning
	}
	return command, fmt.Sprintf("bootstrapping worktrees with the switch hook found at %s", hookPath(launchDir)), ""
}

// discoverHook resolves .marquee/hook into a hook command, or explains why it
// cannot. The command is the script's absolute path, shell-quoted: the hook runs
// with its cwd set to whichever worktree is being bootstrapped, so a path relative
// to the launch checkout would resolve somewhere else — or nowhere — on a switch.
//
// Only an executable regular file counts, and symlinks are followed (os.Stat, not
// os.Lstat) because a symlink into wherever the repo keeps its real bootstrap
// script is the expected way to adopt this. Anything else is left alone with a
// warning: a hook marquee silently declines to run is a bootstrap the operator
// believes happened.
func discoverHook(launchDir string) (command, warning string) {
	path := hookPath(launchDir)
	if _, err := os.Lstat(path); err != nil {
		return "", ""
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Sprintf("ignoring %s: it exists but does not resolve to a file (a dangling symlink?): %v", path, err)
	}
	switch {
	case info.IsDir():
		return "", fmt.Sprintf("ignoring %s: it is a directory, and the switch hook has to be an executable file", path)
	case !info.Mode().IsRegular():
		return "", fmt.Sprintf("ignoring %s: it is not a regular file (mode %s), and the switch hook has to be an executable file", path, info.Mode())
	case info.Mode().Perm()&0o111 == 0:
		return "", fmt.Sprintf("ignoring %s: it is not executable (mode %s); chmod +x it, or pass --switch-hook yourself", path, info.Mode().Perm())
	}
	return shellQuote(path), ""
}

// shellQuote wraps s as a single literal word for the "sh -c" the hook runs
// through, so a checkout under a path with a space or a quote in it still spawns
// one command with no arguments.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
