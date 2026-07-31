package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeHook puts content at .marquee/hook inside a fresh launch directory and
// returns both, so a test names only what it is about.
func writeHook(t *testing.T, mode os.FileMode) (launchDir, path string) {
	t.Helper()
	launchDir = t.TempDir()
	if err := os.Mkdir(filepath.Join(launchDir, conventionDir), 0o755); err != nil {
		t.Fatal(err)
	}
	path = hookPath(launchDir)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), mode); err != nil {
		t.Fatal(err)
	}
	return launchDir, path
}

func TestDiscoverHookFindsAnExecutableFile(t *testing.T) {
	launchDir, path := writeHook(t, 0o755)

	command, warning := discoverHook(launchDir)
	if warning != "" {
		t.Fatalf("warning = %q, want none", warning)
	}
	// The command has to be the absolute path: the hook runs with its cwd set to
	// whichever worktree is being bootstrapped, and a relative path would resolve
	// against that worktree instead of the launch checkout.
	if want := "'" + path + "'"; command != want {
		t.Errorf("command = %q, want %q", command, want)
	}
	if !filepath.IsAbs(strings.Trim(command, "'")) {
		t.Errorf("command %q is not an absolute path", command)
	}
}

// A symlink into wherever the repo already keeps its bootstrap script is the
// expected way to adopt the convention, so a symlink to an executable file counts.
func TestDiscoverHookFollowsASymlinkToAnExecutableFile(t *testing.T) {
	launchDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(launchDir, conventionDir), 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(launchDir, "bootstrap.sh")
	if err := os.WriteFile(real, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, hookPath(launchDir)); err != nil {
		t.Fatal(err)
	}

	command, warning := discoverHook(launchDir)
	if warning != "" {
		t.Fatalf("warning = %q, want none", warning)
	}
	if want := "'" + hookPath(launchDir) + "'"; command != want {
		t.Errorf("command = %q, want %q", command, want)
	}
}

func TestDiscoverHookIsSilentWithoutOne(t *testing.T) {
	command, warning := discoverHook(t.TempDir())
	if command != "" || warning != "" {
		t.Errorf("discoverHook = (%q, %q), want both empty", command, warning)
	}
}

// A .marquee/hook that cannot be run is the case that has to be loud: the
// operator believes their worktree is being bootstrapped, and silence would leave
// the child running against a half-configured environment. None of these shapes
// may be turned into a command.
func TestDiscoverHookRefusesShapesItCannotRun(t *testing.T) {
	dirLaunch := t.TempDir()
	if err := os.MkdirAll(hookPath(dirLaunch), 0o755); err != nil {
		t.Fatal(err)
	}

	symlinkedDirLaunch := t.TempDir()
	if err := os.Mkdir(filepath.Join(symlinkedDirLaunch, conventionDir), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(symlinkedDirLaunch, "scripts")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, hookPath(symlinkedDirLaunch)); err != nil {
		t.Fatal(err)
	}

	danglingLaunch := t.TempDir()
	if err := os.Mkdir(filepath.Join(danglingLaunch, conventionDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(danglingLaunch, "gone.sh"), hookPath(danglingLaunch)); err != nil {
		t.Fatal(err)
	}

	plainLaunch, _ := writeHook(t, 0o644)

	cases := []struct {
		name      string
		launchDir string
		want      string
	}{
		{"a directory", dirLaunch, "directory"},
		{"a symlink to a directory", symlinkedDirLaunch, "directory"},
		{"a dangling symlink", danglingLaunch, "does not resolve"},
		{"a non-executable file", plainLaunch, "not executable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			command, warning := discoverHook(tc.launchDir)
			if command != "" {
				t.Fatalf("command = %q, want none for %s", command, tc.name)
			}
			if !strings.Contains(warning, tc.want) {
				t.Errorf("warning = %q, want it to mention %q", warning, tc.want)
			}
			if !strings.Contains(warning, hookPath(tc.launchDir)) {
				t.Errorf("warning = %q, want it to name the path", warning)
			}
		})
	}
}

func TestResolveSwitchHookPrefersTheExplicitFlag(t *testing.T) {
	launchDir, _ := writeHook(t, 0o755)

	command, notice, warning := resolveSwitchHook("bin/setup", true, launchDir)
	if command != "bin/setup" {
		t.Errorf("command = %q, want the explicit flag value", command)
	}
	if notice != "" || warning != "" {
		t.Errorf("notice/warning = (%q, %q), want none", notice, warning)
	}
}

// An explicit empty --switch-hook is how the convention is turned off, which
// matters most for the shapes that would otherwise warn on every start.
func TestResolveSwitchHookExplicitEmptyIgnoresTheConvention(t *testing.T) {
	launchDir, _ := writeHook(t, 0o755)
	if command, _, _ := resolveSwitchHook("", true, launchDir); command != "" {
		t.Errorf("command = %q, want none", command)
	}

	plainLaunch, _ := writeHook(t, 0o644)
	command, notice, warning := resolveSwitchHook("", true, plainLaunch)
	if command != "" || notice != "" || warning != "" {
		t.Errorf("resolveSwitchHook = (%q, %q, %q), want all empty", command, notice, warning)
	}
}

func TestResolveSwitchHookAnnouncesADiscoveredHook(t *testing.T) {
	launchDir, path := writeHook(t, 0o755)

	command, notice, warning := resolveSwitchHook("", false, launchDir)
	if command == "" {
		t.Fatal("command is empty, want the discovered hook")
	}
	if warning != "" {
		t.Errorf("warning = %q, want none", warning)
	}
	if !strings.Contains(notice, path) {
		t.Errorf("notice = %q, want it to name %q", notice, path)
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"/repo/.marquee/hook":         "'/repo/.marquee/hook'",
		"/my repo/.marquee/hook":      "'/my repo/.marquee/hook'",
		`/it's/mine/.marquee/hook`:    `'/it'\''s/mine/.marquee/hook'`,
		"/repo/.marquee/hook; rm -rf": "'/repo/.marquee/hook; rm -rf'",
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseArgsRecordsWhetherSwitchHookWasGiven(t *testing.T) {
	opts, err := parseArgs("marquee", []string{"--", "bin/dev"}, io.Discard)
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if opts.switchHookSet {
		t.Error("switchHookSet is true without the flag")
	}
	opts, err = parseArgs("marquee", []string{"--switch-hook", "", "--", "bin/dev"}, io.Discard)
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if !opts.switchHookSet {
		t.Error("switchHookSet is false after an explicit empty --switch-hook")
	}
}
