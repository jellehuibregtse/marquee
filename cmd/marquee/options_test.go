package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestParseArgsDefaults(t *testing.T) {
	opts, err := parseArgs("marquee", []string{"--", "bin/dev"}, io.Discard)
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
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
	if opts.noOpen || opts.quiet || opts.unsafeListen {
		t.Errorf("bool flags default true: %+v", opts)
	}
	if len(opts.allowHosts) != 0 {
		t.Errorf("allowHosts = %v, want empty", opts.allowHosts)
	}
	if len(opts.command) != 1 || opts.command[0] != "bin/dev" {
		t.Errorf("command = %v, want [bin/dev]", opts.command)
	}
}

func TestParseArgsAllowHostRepeatable(t *testing.T) {
	opts, err := parseArgs("marquee",
		[]string{"--allow-host", "a.test", "--allow-host", "b.test", "--", "bin/dev"}, io.Discard)
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	want := []string{"a.test", "b.test"}
	if strings.Join(opts.allowHosts, ",") != strings.Join(want, ",") {
		t.Errorf("allowHosts = %v, want %v", opts.allowHosts, want)
	}
}

func TestParseArgsFlagsCaptured(t *testing.T) {
	opts, err := parseArgs("marquee",
		[]string{"--position", "top-left", "--quiet", "--no-open", "--unsafe-listen",
			"--listen", "0.0.0.0:3000", "--", "bin/dev", "arg"}, io.Discard)
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if opts.position != "top-left" || !opts.quiet || !opts.noOpen || !opts.unsafeListen {
		t.Errorf("flags not captured: %+v", opts)
	}
	if opts.listen != "0.0.0.0:3000" {
		t.Errorf("listen = %q, want 0.0.0.0:3000", opts.listen)
	}
	if len(opts.command) != 2 || opts.command[0] != "bin/dev" || opts.command[1] != "arg" {
		t.Errorf("command = %v, want [bin/dev arg]", opts.command)
	}
}

func TestParseArgsPositionCorners(t *testing.T) {
	for _, corner := range []string{"bottom-left", "bottom-right", "top-left", "top-right"} {
		opts, err := parseArgs("marquee", []string{"--position", corner, "--", "bin/dev"}, io.Discard)
		if err != nil {
			t.Fatalf("parseArgs(--position %s): %v", corner, err)
		}
		if opts.position != corner {
			t.Errorf("position = %q, want %q", opts.position, corner)
		}
	}
}

func TestParseArgsInvalidPosition(t *testing.T) {
	var buf bytes.Buffer
	_, err := parseArgs("marquee", []string{"--position", "sideways", "--", "bin/dev"}, &buf)
	if err == nil {
		t.Fatal("parseArgs accepted an invalid --position")
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

func TestParseArgsSizePresets(t *testing.T) {
	for _, size := range []string{"small", "medium", "large"} {
		opts, err := parseArgs("marquee", []string{"--size", size, "--", "bin/dev"}, io.Discard)
		if err != nil {
			t.Fatalf("parseArgs(--size %s): %v", size, err)
		}
		if opts.size != size {
			t.Errorf("size = %q, want %q", opts.size, size)
		}
	}
}

func TestParseArgsInvalidSize(t *testing.T) {
	var buf bytes.Buffer
	_, err := parseArgs("marquee", []string{"--size", "huge", "--", "bin/dev"}, &buf)
	if err == nil {
		t.Fatal("parseArgs accepted an invalid --size")
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

func TestParseArgsMissingCommand(t *testing.T) {
	_, err := parseArgs("marquee", nil, io.Discard)
	if err == nil {
		t.Fatal("parseArgs accepted an empty command")
	}
}

func TestParseArgsVersionNeedsNoCommand(t *testing.T) {
	opts, err := parseArgs("marquee", []string{"--version"}, io.Discard)
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if !opts.showVersion {
		t.Error("showVersion not set")
	}
}

func TestValidateListenLoopback(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:3000", "localhost:3000", "[::1]:3000", "app.localhost:3000"} {
		unsafe, err := validateListen(addr, false)
		if err != nil {
			t.Errorf("validateListen(%q, false) = %v, want no error", addr, err)
		}
		if unsafe {
			t.Errorf("validateListen(%q, false) reported unsafe, want false", addr)
		}
	}
}

func TestValidateListenNonLoopbackRefusedWithoutFlag(t *testing.T) {
	_, err := validateListen("0.0.0.0:3000", false)
	if err == nil {
		t.Fatal("non-loopback listen accepted without --unsafe-listen")
	}
	if !strings.Contains(err.Error(), "--unsafe-listen") {
		t.Errorf("refusal does not mention --unsafe-listen: %v", err)
	}
}

func TestValidateListenNonLoopbackAllowedWithFlag(t *testing.T) {
	unsafe, err := validateListen("0.0.0.0:3000", true)
	if err != nil {
		t.Fatalf("validateListen with --unsafe-listen = %v, want accepted", err)
	}
	if !unsafe {
		t.Error("validateListen did not report unsafe exposure for a non-loopback address")
	}
}

func TestValidateListenInvalidAddress(t *testing.T) {
	if _, err := validateListen("garbage", false); err == nil {
		t.Fatal("validateListen accepted a malformed address")
	}
}

func TestUnsafeListenWarningIsLoud(t *testing.T) {
	var buf bytes.Buffer
	printUnsafeListenWarning(&buf, "0.0.0.0:3000")
	out := buf.String()
	if !strings.Contains(out, "0.0.0.0:3000") {
		t.Errorf("warning omits the address: %q", out)
	}
	if !strings.Contains(strings.ToLower(out), "network") {
		t.Errorf("warning does not mention network exposure: %q", out)
	}
}

// TestUnsafeBannerNotSuppressibleByQuiet is the abuse assertion for the
// escape hatch: --quiet must never be able to silence the network-exposure
// banner. The banner is written straight to its io.Writer, never routed
// through the logger, so a --quiet logger (which swallows every Info line)
// has no path to suppress it. The test proves both halves in one buffer: the
// quiet logger's Info leaves nothing, then each banner still lands.
func TestUnsafeBannerNotSuppressibleByQuiet(t *testing.T) {
	var buf bytes.Buffer
	log := newLogger(&buf, true)
	log.Info("listening on %s", "0.0.0.0:3000")
	if buf.Len() != 0 {
		t.Fatalf("quiet logger emitted an info line: %q", buf.String())
	}

	printUnsafeListenWarning(&buf, "0.0.0.0:3000")
	if !strings.Contains(buf.String(), "0.0.0.0:3000") {
		t.Errorf("--quiet suppressed the listen banner: %q", buf.String())
	}

	buf.Reset()
	printUnsafeUpstreamWarning(&buf, "http://192.168.1.5:3100")
	if !strings.Contains(buf.String(), "192.168.1.5:3100") {
		t.Errorf("--quiet suppressed the upstream banner: %q", buf.String())
	}
}

func TestBrowserURL(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:3000": "http://127.0.0.1:3000",
		"localhost:8080": "http://localhost:8080",
		"0.0.0.0:3000":   "http://127.0.0.1:3000",
		":3000":          "http://127.0.0.1:3000",
		"[::]:3000":      "http://127.0.0.1:3000",
	}
	for listen, want := range cases {
		if got := browserURL(listen); got != want {
			t.Errorf("browserURL(%q) = %q, want %q", listen, got, want)
		}
	}
}

func TestBrowserCommand(t *testing.T) {
	if got := browserCommand("darwin", "http://x").Args[0]; got != "open" {
		t.Errorf("darwin opener = %q, want open", got)
	}
	if got := browserCommand("linux", "http://x").Args[0]; got != "xdg-open" {
		t.Errorf("linux opener = %q, want xdg-open", got)
	}
}
