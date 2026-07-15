package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jellehuibregtse/marquee/internal/gitinfo"
	"github.com/jellehuibregtse/marquee/internal/proxy"
	"github.com/jellehuibregtse/marquee/internal/status"
)

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", raw, err)
	}
	return u
}

func TestParseAttachArgsDefaults(t *testing.T) {
	opts, err := parseAttachArgs("marquee attach", []string{"--upstream", "http://localhost:3100"}, io.Discard)
	if err != nil {
		t.Fatalf("parseAttachArgs: %v", err)
	}
	if opts.listen != "127.0.0.1:3000" {
		t.Errorf("listen = %q, want 127.0.0.1:3000", opts.listen)
	}
	if opts.position != "bottom-left" {
		t.Errorf("position = %q, want bottom-left", opts.position)
	}
	if opts.size != "medium" {
		t.Errorf("size = %q, want medium", opts.size)
	}
	if opts.theme != "default" {
		t.Errorf("theme = %q, want default", opts.theme)
	}
	if got := strings.Join(opts.pills, ","); got != "branch,dirty,worktree,pr" {
		t.Errorf("pills = %q, want branch,dirty,worktree,pr", got)
	}
	if opts.upstreamURL == nil || opts.upstreamURL.Host != "localhost:3100" {
		t.Errorf("upstreamURL = %v, want host localhost:3100", opts.upstreamURL)
	}
}

func TestParseAttachArgsFlagsCaptured(t *testing.T) {
	opts, err := parseAttachArgs("marquee attach", []string{
		"--upstream", "http://127.0.0.1:9999", "--listen", "localhost:4000",
		"--position", "top-right", "--quiet", "--no-open", "--unsafe-listen",
		"--allow-host", "a.test", "--allow-host", "b.test",
	}, io.Discard)
	if err != nil {
		t.Fatalf("parseAttachArgs: %v", err)
	}
	if opts.listen != "localhost:4000" || opts.position != "top-right" {
		t.Errorf("flags not captured: %+v", opts)
	}
	if !opts.quiet || !opts.noOpen || !opts.unsafeListen {
		t.Errorf("bool flags not captured: %+v", opts)
	}
	if strings.Join(opts.allowHosts, ",") != "a.test,b.test" {
		t.Errorf("allowHosts = %v", opts.allowHosts)
	}
}

func TestParseAttachArgsPositionCorners(t *testing.T) {
	for _, corner := range []string{"bottom-left", "bottom-right", "top-left", "top-right"} {
		opts, err := parseAttachArgs("marquee attach",
			[]string{"--upstream", "http://localhost:3100", "--position", corner}, io.Discard)
		if err != nil {
			t.Fatalf("parseAttachArgs(--position %s): %v", corner, err)
		}
		if opts.position != corner {
			t.Errorf("position = %q, want %q", opts.position, corner)
		}
	}
}

func TestParseAttachArgsInvalidPosition(t *testing.T) {
	var buf bytes.Buffer
	_, err := parseAttachArgs("marquee attach",
		[]string{"--upstream", "http://localhost:3100", "--position", "sideways"}, &buf)
	if err == nil {
		t.Fatal("parseAttachArgs accepted an invalid --position")
	}
	out := buf.String()
	if !strings.Contains(out, "invalid --position") {
		t.Errorf("missing error message: %q", out)
	}
	for _, corner := range []string{"bottom-left", "bottom-right", "top-left", "top-right"} {
		if !strings.Contains(out, corner) {
			t.Errorf("error message does not list %q: %q", corner, out)
		}
	}
}

func TestParseAttachArgsSizePresets(t *testing.T) {
	for _, size := range []string{"small", "medium", "large"} {
		opts, err := parseAttachArgs("marquee attach",
			[]string{"--upstream", "http://localhost:3100", "--size", size}, io.Discard)
		if err != nil {
			t.Fatalf("parseAttachArgs(--size %s): %v", size, err)
		}
		if opts.size != size {
			t.Errorf("size = %q, want %q", opts.size, size)
		}
	}
}

func TestParseAttachArgsInvalidSize(t *testing.T) {
	var buf bytes.Buffer
	_, err := parseAttachArgs("marquee attach",
		[]string{"--upstream", "http://localhost:3100", "--size", "huge"}, &buf)
	if err == nil {
		t.Fatal("parseAttachArgs accepted an invalid --size")
	}
	out := buf.String()
	if !strings.Contains(out, "invalid --size") {
		t.Errorf("missing error message: %q", out)
	}
	for _, size := range []string{"small", "medium", "large"} {
		if !strings.Contains(out, size) {
			t.Errorf("error message does not list %q: %q", size, out)
		}
	}
}

func TestParseAttachArgsThemes(t *testing.T) {
	for _, theme := range []string{"default", "midnight", "sand", "forest"} {
		opts, err := parseAttachArgs("marquee attach",
			[]string{"--upstream", "http://localhost:3100", "--theme", theme}, io.Discard)
		if err != nil {
			t.Fatalf("parseAttachArgs(--theme %s): %v", theme, err)
		}
		if opts.theme != theme {
			t.Errorf("theme = %q, want %q", opts.theme, theme)
		}
	}
}

func TestParseAttachArgsInvalidTheme(t *testing.T) {
	var buf bytes.Buffer
	_, err := parseAttachArgs("marquee attach",
		[]string{"--upstream", "http://localhost:3100", "--theme", "neon"}, &buf)
	if err == nil {
		t.Fatal("parseAttachArgs accepted an invalid --theme")
	}
	out := buf.String()
	if !strings.Contains(out, "invalid --theme") {
		t.Errorf("missing error message: %q", out)
	}
	for _, theme := range []string{"default", "midnight", "sand", "forest"} {
		if !strings.Contains(out, theme) {
			t.Errorf("error message does not list %q: %q", theme, out)
		}
	}
}

func TestParseAttachArgsPillsSubsetPreservesOrder(t *testing.T) {
	opts, err := parseAttachArgs("marquee attach",
		[]string{"--upstream", "http://localhost:3100", "--pills", "branch,pr"}, io.Discard)
	if err != nil {
		t.Fatalf("parseAttachArgs(--pills): %v", err)
	}
	if got := strings.Join(opts.pills, ","); got != "branch,pr" {
		t.Errorf("pills = %q, want branch,pr", got)
	}
}

func TestParseAttachArgsPillsEmptyHidesAll(t *testing.T) {
	opts, err := parseAttachArgs("marquee attach",
		[]string{"--upstream", "http://localhost:3100", "--pills", ""}, io.Discard)
	if err != nil {
		t.Fatalf("parseAttachArgs(--pills \"\"): %v", err)
	}
	if len(opts.pills) != 0 {
		t.Errorf("pills = %v, want empty", opts.pills)
	}
}

func TestParseAttachArgsInvalidPill(t *testing.T) {
	var buf bytes.Buffer
	_, err := parseAttachArgs("marquee attach",
		[]string{"--upstream", "http://localhost:3100", "--pills", "branch,nope"}, &buf)
	if err == nil {
		t.Fatal("parseAttachArgs accepted an unknown pill")
	}
	out := buf.String()
	if !strings.Contains(out, "invalid --pills") {
		t.Errorf("missing error message: %q", out)
	}
	for _, id := range []string{"branch", "dirty", "worktree", "pr"} {
		if !strings.Contains(out, id) {
			t.Errorf("error message does not list %q: %q", id, out)
		}
	}
}

func TestParseAttachArgsDuplicatePill(t *testing.T) {
	var buf bytes.Buffer
	_, err := parseAttachArgs("marquee attach",
		[]string{"--upstream", "http://localhost:3100", "--pills", "pr,pr"}, &buf)
	if err == nil {
		t.Fatal("parseAttachArgs accepted a duplicate pill")
	}
	out := buf.String()
	if !strings.Contains(out, "invalid --pills") {
		t.Errorf("missing error message: %q", out)
	}
	for _, id := range []string{"branch", "dirty", "worktree", "pr"} {
		if !strings.Contains(out, id) {
			t.Errorf("error message does not list %q: %q", id, out)
		}
	}
}

func TestParseAttachArgsUpstreamRequired(t *testing.T) {
	var buf bytes.Buffer
	if _, err := parseAttachArgs("marquee attach", nil, &buf); err == nil {
		t.Fatal("parseAttachArgs accepted a missing --upstream")
	}
	if !strings.Contains(buf.String(), "--upstream is required") {
		t.Errorf("missing error message: %q", buf.String())
	}
}

func TestParseAttachArgsBadScheme(t *testing.T) {
	for _, raw := range []string{"ftp://localhost:3100", "localhost:3100", "://nope"} {
		var buf bytes.Buffer
		if _, err := parseAttachArgs("marquee attach", []string{"--upstream", raw}, &buf); err == nil {
			t.Errorf("parseAttachArgs accepted a bad --upstream %q", raw)
		}
	}
}

func TestParseAttachArgsRejectsPositional(t *testing.T) {
	var buf bytes.Buffer
	if _, err := parseAttachArgs("marquee attach", []string{"--upstream", "http://localhost:3100", "bin/dev"}, &buf); err == nil {
		t.Fatal("parseAttachArgs accepted a positional argument")
	}
	if !strings.Contains(buf.String(), "positional") {
		t.Errorf("missing error message: %q", buf.String())
	}
}

func TestValidateUpstreamLoopback(t *testing.T) {
	for _, raw := range []string{
		"http://localhost:3100", "http://127.0.0.1:3100", "http://[::1]:3100",
		"http://app.localhost:3100", "https://localhost",
	} {
		unsafe, err := validateUpstream(mustParseURL(t, raw), false)
		if err != nil {
			t.Errorf("validateUpstream(%q, false) = %v, want no error", raw, err)
		}
		if unsafe {
			t.Errorf("validateUpstream(%q, false) reported unsafe, want false", raw)
		}
	}
}

// TestValidateUpstreamNonLoopbackRefused is the abuse test: a non-loopback
// upstream must be refused and the refusal must take no network action.
// validateUpstream only inspects the host string (loopbackHost), so a
// remote host is rejected without ever dialing it. The lookalike cases pin
// the tricks a naive check would fall for: userinfo that makes a remote host
// read as loopback (127.0.0.1@evil.com — Hostname() is evil.com, not
// 127.0.0.1), the decimal form of 127.0.0.1 (2130706433, which the OS
// resolver would dial as loopback but marquee refuses because it is not a
// canonical loopback literal), and a localhost-prefixed suffix domain.
func TestValidateUpstreamNonLoopbackRefused(t *testing.T) {
	for _, raw := range []string{
		"http://192.168.1.5:3100", "http://example.test:3100", "http://10.0.0.1",
		"http://127.0.0.1@evil.com", "http://localhost@evil.com",
		"http://2130706433:3100", "http://localhost.evil.com:3100",
	} {
		_, err := validateUpstream(mustParseURL(t, raw), false)
		if err == nil {
			t.Errorf("validateUpstream(%q, false) accepted a non-loopback upstream", raw)
			continue
		}
		if !strings.Contains(err.Error(), "--unsafe-listen") {
			t.Errorf("refusal for %q does not mention --unsafe-listen: %v", raw, err)
		}
	}
}

func TestValidateUpstreamNonLoopbackAllowedWithFlag(t *testing.T) {
	unsafe, err := validateUpstream(mustParseURL(t, "http://192.168.1.5:3100"), true)
	if err != nil {
		t.Fatalf("validateUpstream with --unsafe-listen = %v, want accepted", err)
	}
	if !unsafe {
		t.Error("validateUpstream did not report unsafe exposure for a non-loopback upstream")
	}
}

func TestUnsafeUpstreamWarningIsLoud(t *testing.T) {
	var buf bytes.Buffer
	printUnsafeUpstreamWarning(&buf, "http://192.168.1.5:3100")
	out := buf.String()
	if !strings.Contains(out, "192.168.1.5:3100") {
		t.Errorf("warning omits the upstream: %q", out)
	}
	if !strings.Contains(strings.ToLower(out), "unsafe") {
		t.Errorf("warning does not read as a warning: %q", out)
	}
}

func TestUpstreamDialAddr(t *testing.T) {
	cases := map[string]string{
		"http://localhost:3100": "localhost:3100",
		"http://localhost":      "localhost:80",
		"https://localhost":     "localhost:443",
		"http://127.0.0.1:9":    "127.0.0.1:9",
	}
	for raw, want := range cases {
		if got := upstreamDialAddr(mustParseURL(t, raw)); got != want {
			t.Errorf("upstreamDialAddr(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestAttachProxiesAndInjects wires an attach-mode handler exactly as
// runAttach does — proxy.New with an explicit UpstreamURL plus
// status.Register — against a loopback HTML upstream, then asserts the bar
// is injected, /__marquee/status serves valid JSON, and /__marquee/bar.js
// serves.
func TestAttachProxiesAndInjects(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<html><body><h1>app</h1></body></html>")
	}))
	defer upstream.Close()

	handler := proxy.New(proxy.Config{UpstreamURL: mustParseURL(t, upstream.URL)})
	status.Register(handler.Internal(), status.Deps{
		Git:        func() gitinfo.Snapshot { return gitinfo.Snapshot{Branch: "main"} },
		ChildState: func() string { return "attached" },
		Position:   "bottom",
	})
	proxySrv := httptest.NewServer(handler)
	defer proxySrv.Close()

	resp, err := http.Get(proxySrv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("page status = %d, want 200", resp.StatusCode)
	}
	snippet := `<script type="module" src="/__marquee/bar.js"></script><marquee-bar></marquee-bar>`
	if !strings.Contains(string(body), snippet) {
		t.Fatalf("bar snippet not injected: %q", body)
	}
	if idx := strings.Index(string(body), snippet); idx == -1 || idx > strings.LastIndex(string(body), "</body>") {
		t.Fatalf("bar snippet not spliced before </body>: %q", body)
	}

	statusReq, _ := http.NewRequest(http.MethodGet, proxySrv.URL+"/__marquee/status", nil)
	statusReq.Host = "localhost"
	statusResp, err := http.DefaultClient.Do(statusReq)
	if err != nil {
		t.Fatal(err)
	}
	statusBody, err := io.ReadAll(statusResp.Body)
	_ = statusResp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("status endpoint = %d, want 200", statusResp.StatusCode)
	}
	var got map[string]any
	if err := json.Unmarshal(statusBody, &got); err != nil {
		t.Fatalf("status is not valid JSON: %v (%q)", err, statusBody)
	}
	if got["child"].(map[string]any)["state"] != "attached" {
		t.Errorf("child state = %v, want attached", got["child"])
	}

	barReq, _ := http.NewRequest(http.MethodGet, proxySrv.URL+"/__marquee/bar.js", nil)
	barReq.Host = "localhost"
	barResp, err := http.DefaultClient.Do(barReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = barResp.Body.Close()
	if barResp.StatusCode != http.StatusOK {
		t.Fatalf("bar.js = %d, want 200", barResp.StatusCode)
	}
}
