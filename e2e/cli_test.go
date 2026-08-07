package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// isolatedCacheEnv moves the user cache directory into dir, which is where the
// session file lands. Both variables are set because os.UserCacheDir reads
// XDG_CACHE_HOME on Linux and $HOME/Library/Caches on macOS — and without this
// a test would write a session file into the developer's own cache, where
// `marquee switch` run with no --listen could find it and restart their real
// dev server.
func isolatedCacheEnv(dir string) []string {
	return []string{"HOME=" + dir, "XDG_CACHE_HOME=" + filepath.Join(dir, "cache")}
}

// runCLI invokes a marquee subcommand against the same isolated cache the
// marquee under test was started with, from cwd.
func runCLI(t *testing.T, cacheHome, cwd string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(marqueeBin, args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), isolatedCacheEnv(cacheHome)...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("running %v: %v", args, err)
	}
	return stdout.String(), stderr.String(), code
}

// startSwitchableMarquee builds a repo with a second worktree and wraps
// testupstream in the main one, with an isolated cache directory. It returns
// the two worktree paths and the cache home the CLI must be pointed at.
func startSwitchableMarquee(t *testing.T) (proc *marqueeProc, mainWt, featureWt, cacheHome string) {
	t.Helper()
	tmp := t.TempDir()
	mainWt = filepath.Join(tmp, "main")
	featureWt = filepath.Join(tmp, "feature")
	cacheHome = filepath.Join(tmp, "home")
	if err := os.MkdirAll(cacheHome, 0o700); err != nil {
		t.Fatalf("cache home: %v", err)
	}
	if err := makeSwitchRepo(mainWt, featureWt, "marquee-e2e-cli"); err != nil {
		t.Fatalf("build repo: %v", err)
	}
	proc, err := startMarqueeWith(mainWt, nil, []string{upstreamBin}, isolatedCacheEnv(cacheHome)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.stop() })
	if err := proc.waitHealthy(15 * time.Second); err != nil {
		t.Fatal(err)
	}
	return proc, mainWt, featureWt, cacheHome
}

// TestStatusSubcommandFindsTheMarqueeForThisRepository is the whole point of the
// session file: run from inside a worktree, with no address given, the CLI finds
// the marquee serving it.
func TestStatusSubcommandFindsTheMarqueeForThisRepository(t *testing.T) {
	proc, mainWt, _, cacheHome := startSwitchableMarquee(t)

	stdout, stderr, code := runCLI(t, cacheHome, mainWt, "status")
	if code != 0 {
		t.Fatalf("status exited %d; stderr %s", code, stderr)
	}
	for _, want := range []string{proc.addr, "main", "feature"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status output does not mention %q:\n%s", want, stdout)
		}
	}
}

// TestStatusSubcommandJSONIsTheEndpointPayload keeps `--json` a passthrough of
// the server's own bytes, which is what lets a script read a field the CLI does
// not render.
func TestStatusSubcommandJSONIsTheEndpointPayload(t *testing.T) {
	_, mainWt, _, cacheHome := startSwitchableMarquee(t)

	stdout, stderr, code := runCLI(t, cacheHome, mainWt, "status", "--json")
	if code != 0 {
		t.Fatalf("status --json exited %d; stderr %s", code, stderr)
	}
	var payload struct {
		Branch    string `json:"branch"`
		Worktrees []struct {
			Slug string `json:"slug"`
		} `json:"worktrees"`
		Catalog map[string]any `json:"catalog"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("status --json is not JSON: %v\n%s", err, stdout)
	}
	if payload.Branch == "" || len(payload.Worktrees) != 2 {
		t.Errorf("payload = %+v, want a branch and two worktrees", payload)
	}
	if len(payload.Catalog) == 0 {
		t.Error("payload dropped the knob catalog, so --json is not the endpoint's own bytes")
	}
}

// TestSwitchSubcommandRestartsInTheTargetWorktree is the end-to-end contract:
// the CLI reads the session file for the token and drives the same endpoint the
// bar does, and afterwards the status reports the new worktree.
func TestSwitchSubcommandRestartsInTheTargetWorktree(t *testing.T) {
	_, mainWt, _, cacheHome := startSwitchableMarquee(t)

	stdout, stderr, code := runCLI(t, cacheHome, mainWt, "switch", "feature")
	if code != 0 {
		t.Fatalf("switch exited %d; stdout %s stderr %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "switched to feature") {
		t.Errorf("switch stdout = %q, want it to confirm the switch", stdout)
	}

	after, _, code := runCLI(t, cacheHome, mainWt, "status", "--json")
	if code != 0 {
		t.Fatalf("status after switch exited %d", code)
	}
	var payload struct {
		Worktree struct {
			Slug string `json:"slug"`
		} `json:"worktree"`
	}
	if err := json.Unmarshal([]byte(after), &payload); err != nil {
		t.Fatalf("status --json: %v", err)
	}
	if payload.Worktree.Slug != "feature" {
		t.Errorf("worktree after switch = %q, want feature", payload.Worktree.Slug)
	}
}

// TestSwitchSubcommandUnknownSlugListsTheTargets is the message a typo has to
// produce: naming the real worktrees is the difference between a usable command
// and one that leaves the caller guessing.
func TestSwitchSubcommandUnknownSlugListsTheTargets(t *testing.T) {
	_, mainWt, _, cacheHome := startSwitchableMarquee(t)

	_, stderr, code := runCLI(t, cacheHome, mainWt, "switch", "featrue")
	if code == 0 {
		t.Fatal("switch to an unknown worktree exited 0")
	}
	for _, want := range []string{"featrue", "main", "feature"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("error %q does not mention %q", stderr, want)
		}
	}
}

// TestClientSubcommandsWithNoMarqueeRunning covers the common first contact:
// nothing is running, and the message says how to start one instead of dumping
// a connection error.
func TestClientSubcommandsWithNoMarqueeRunning(t *testing.T) {
	tmp := t.TempDir()
	for _, args := range [][]string{{"status"}, {"switch", "anything"}} {
		_, stderr, code := runCLI(t, tmp, tmp, args...)
		if code != 1 {
			t.Errorf("%v exited %d, want 1", args, code)
		}
		if !strings.Contains(stderr, "no marquee is running") {
			t.Errorf("%v said %q, want it to report that none is running", args, stderr)
		}
	}
}

// TestSessionFileRemovedOnShutdown keeps the artifact's lifetime tied to the
// process's: a file left behind would send the next CLI run at an address
// nothing answers on.
func TestSessionFileRemovedOnShutdown(t *testing.T) {
	proc, _, _, cacheHome := startSwitchableMarquee(t)

	sessions := findSessionFiles(t, cacheHome)
	if len(sessions) != 1 {
		t.Fatalf("found %d session files while running, want 1", len(sessions))
	}
	if err := proc.stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if left := findSessionFiles(t, cacheHome); len(left) != 0 {
		t.Errorf("session files left after shutdown: %v", left)
	}
}

func findSessionFiles(t *testing.T, cacheHome string) []string {
	t.Helper()
	var found []string
	for _, root := range []string{
		filepath.Join(cacheHome, "Library", "Caches", "marquee"),
		filepath.Join(cacheHome, "cache", "marquee"),
	} {
		matches, err := filepath.Glob(filepath.Join(root, "*.json"))
		if err != nil {
			t.Fatalf("glob: %v", err)
		}
		found = append(found, matches...)
	}
	return found
}
