package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/jellehuibregtse/marquee/internal/gitinfo"
	"github.com/jellehuibregtse/marquee/internal/hook"
	"github.com/jellehuibregtse/marquee/internal/knob"
	"github.com/jellehuibregtse/marquee/internal/switcher"
)

// errUsage signals a usage problem whose message parseArgs has already
// written; run turns it into exit code 2 without printing anything more.
var errUsage = errors.New("usage error")

// The usage strings stay as prose; the accepted values and their validation
// come from the knob catalog (knob.Default), the single owner of every knob's
// value set, so a flag can never accept an id the bar has no rendering for.
const positionUsage = "where the bar renders: bottom-left, bottom-right, top-left, or top-right"

const sizeUsage = "how large the bar renders: small, medium, or large"

const themeUsage = "the bar's color theme: default, midnight, sand, or forest"

// openUsage documents the opt-in browser launch. marquee is a long-lived proxy in
// front of a dev stack, usually started once per day from a process manager or a
// terminal that is then left alone, so a browser window it opens by itself is a
// window nobody asked for.
const openUsage = "open the browser once the app is healthy (off unless asked for)"

// pillsUsage documents the --pills list. The list order is the render order, an
// omitted id is hidden, and an empty value hides all pills.
const pillsUsage = "which info pills to show, comma-separated over branch,dirty,worktree,pr (list order = render order; omit an id to hide it; empty hides all)"

// parsePills turns the --pills CSV into an ordered slice. An empty value is
// valid and yields no pills (all hidden). An unknown id or a duplicate is a
// usage error whose message lists the valid ids from the catalog.
func parsePills(raw string) ([]string, error) {
	if raw == "" {
		return []string{}, nil
	}
	parts := strings.Split(raw, ",")
	pills := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, part := range parts {
		id := strings.TrimSpace(part)
		if !knob.Default.Pills.Valid(id) {
			return nil, fmt.Errorf("invalid --pills %q: unknown pill %q: must be one of %s", raw, id, knob.Default.Pills.List())
		}
		if seen[id] {
			return nil, fmt.Errorf("invalid --pills %q: duplicate pill %q; each of %s may appear at most once", raw, id, knob.Default.Pills.List())
		}
		seen[id] = true
		pills = append(pills, id)
	}
	return pills, nil
}

// checkTimeout rejects a non-positive switch timeout. Zero and negative are
// both refused rather than read as "no limit": the orchestrator falls back to
// its built-in default for any non-positive value, so an operator who writes
// --hook-timeout 0 hoping to lift the ceiling would silently get the built-in one, and a
// negative one would abort the hook the instant it starts.
func checkTimeout(name string, d time.Duration) error {
	if d <= 0 {
		return fmt.Errorf("invalid %s %s: must be greater than zero", name, d)
	}
	return nil
}

type options struct {
	listen          string
	internalPort    int
	position        string
	size            string
	theme           string
	pillsRaw        string
	pills           []string
	open            bool
	quiet           bool
	allowHosts      []string
	unsafeListen    bool
	keepCSP         bool
	switchHook      string
	switchHookSet   bool
	readyCmd        string
	worktreeGlobs   []string
	worktreeFilter  gitinfo.WorktreeFilter
	hookTimeout     time.Duration
	hookIdleTimeout time.Duration
	healthTimeout   time.Duration
	restartTimeout  time.Duration
	showVersion     bool
	command         []string
}

// stringList collects a repeatable string flag (e.g. --allow-host a
// --allow-host b) into a slice in the order given.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

// newRunFlagSet registers the wrapper-mode flags. It is its own function because
// the set is built twice per run: once over .marquee/config's words alone, so a
// flag the set does not have can be reported against the line it was written on,
// and once for real over those words followed by the command line. Both go through
// here, so the check can never disagree with the flags that actually exist.
func newRunFlagSet(name string, out io.Writer) (*flag.FlagSet, *options) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(out)
	opts := &options{}
	fs.StringVar(&opts.listen, "listen", "127.0.0.1:3000", "address to listen on (loopback only unless --unsafe-listen)")
	fs.IntVar(&opts.internalPort, "internal-port", 0, "port the child binds to (0 picks a free port)")
	fs.StringVar(&opts.position, "position", knob.Default.Positions.Default, positionUsage)
	fs.StringVar(&opts.size, "size", knob.Default.Sizes.Default, sizeUsage)
	fs.StringVar(&opts.theme, "theme", knob.Default.Themes.Default, themeUsage)
	fs.StringVar(&opts.pillsRaw, "pills", knob.Default.Pills.Default, pillsUsage)
	fs.BoolVar(&opts.open, "open", false, openUsage)
	fs.BoolVar(&opts.quiet, "quiet", false, "suppress marquee's informational output (warnings and errors still print)")
	fs.Var((*stringList)(&opts.allowHosts), "allow-host", "extra Host accepted on /__marquee/* endpoints; exact or *.suffix wildcard, e.g. *.lvh.me (repeatable)")
	fs.BoolVar(&opts.unsafeListen, "unsafe-listen", false, "allow a non-loopback --listen, exposing the proxy to the network")
	fs.BoolVar(&opts.keepCSP, "keep-csp", false, "leave the app's Content-Security-Policy untouched (the bar may not load if its CSP forbids same-origin scripts)")
	fs.StringVar(&opts.switchHook, "switch-hook", "", "command run in a worktree before the child starts there, including at startup in the worktree marquee is launched in (e.g. \"bundle install\"); defaults to .marquee/hook when that is executable, and an empty value disables it")
	fs.Var((*stringList)(&opts.worktreeGlobs), "worktree-glob", "glob matched against a worktree's absolute path; when given, only matching worktrees (plus the main one) are switch targets (repeatable)")
	fs.StringVar(&opts.readyCmd, "ready-cmd", "", "command retried in the target worktree after the child's port answers, until it exits 0 or --health-timeout expires (e.g. \"curl -sf localhost:3036\"); empty disables it")
	fs.DurationVar(&opts.hookTimeout, "hook-timeout", hook.DefaultTimeout, "absolute ceiling on one --switch-hook run before its process group is killed; a hook that goes quiet is killed long before this, e.g. 90m")
	fs.DurationVar(&opts.hookIdleTimeout, "hook-idle-timeout", hook.DefaultIdleTimeout, "how long a --switch-hook run may print nothing at all before its process group is killed; raise it for a bootstrap with a longer silent step, e.g. 20m")
	fs.DurationVar(&opts.healthTimeout, "health-timeout", switcher.DefaultHealthTimeout, "how long to wait for a restarted child to become healthy before the switch reverts, e.g. 90s")
	fs.DurationVar(&opts.restartTimeout, "restart-timeout", switcher.DefaultRestartTimeout, "how long a single stop-and-spawn of the child may take, e.g. 60s")
	fs.BoolVar(&opts.showVersion, "version", false, "print version and exit")
	// The usage text follows fs.Output() rather than the writer captured here, so a
	// caller that silences the set while parsing (checkConfigFlags) can still print
	// the usage dump afterwards by handing the set a real writer.
	fs.Usage = func() {
		w := fs.Output()
		_, _ = fmt.Fprintln(w, "usage: marquee [flags] -- command [args...]")
		_, _ = fmt.Fprintln(w, "       marquee attach --upstream <url> [flags]   (proxy a server you run yourself)")
		fs.PrintDefaults()
	}
	return fs, opts
}

func parseArgs(name string, args []string, out io.Writer) (*options, error) {
	fs, opts := newRunFlagSet(name, out)
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	opts.command = fs.Args()
	// Whether --switch-hook was given at all is the difference between "use the
	// conventional .marquee/hook" and "run no hook", since both read as an empty
	// command string.
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "switch-hook" {
			opts.switchHookSet = true
		}
	})

	if !knob.Default.Positions.Valid(opts.position) {
		_, _ = fmt.Fprintf(out, "marquee: invalid --position %q: must be one of %s\n", opts.position, knob.Default.Positions.List())
		return nil, errUsage
	}
	if !knob.Default.Sizes.Valid(opts.size) {
		_, _ = fmt.Fprintf(out, "marquee: invalid --size %q: must be one of %s\n", opts.size, knob.Default.Sizes.List())
		return nil, errUsage
	}
	if !knob.Default.Themes.Valid(opts.theme) {
		_, _ = fmt.Fprintf(out, "marquee: invalid --theme %q: must be one of %s\n", opts.theme, knob.Default.Themes.List())
		return nil, errUsage
	}
	pills, err := parsePills(opts.pillsRaw)
	if err != nil {
		_, _ = fmt.Fprintf(out, "marquee: %v\n", err)
		return nil, errUsage
	}
	opts.pills = pills
	filter, err := gitinfo.NewWorktreeFilter(opts.worktreeGlobs)
	if err != nil {
		_, _ = fmt.Fprintf(out, "marquee: invalid --worktree-glob: %v\n", err)
		return nil, errUsage
	}
	opts.worktreeFilter = filter
	for _, tf := range []struct {
		name  string
		value time.Duration
	}{
		{"--hook-timeout", opts.hookTimeout},
		{"--hook-idle-timeout", opts.hookIdleTimeout},
		{"--health-timeout", opts.healthTimeout},
		{"--restart-timeout", opts.restartTimeout},
	} {
		if err := checkTimeout(tf.name, tf.value); err != nil {
			_, _ = fmt.Fprintf(out, "marquee: %v\n", err)
			return nil, errUsage
		}
	}
	// An idle period longer than the ceiling can never fire, so the operator who
	// raised it would still get the hook killed at the ceiling and be told the whole
	// bootstrap was too long, which is the wrong diagnosis. Refuse it here instead:
	// whoever needs a longer silence almost always needs a longer run too, and
	// raising one without the other is a mistake worth naming. Equal values are
	// allowed; both bounds then land together and either message is true.
	if opts.hookIdleTimeout > opts.hookTimeout {
		_, _ = fmt.Fprintf(out, "marquee: invalid --hook-idle-timeout %s: it exceeds the --hook-timeout ceiling of %s, so it could never fire; raise --hook-timeout too\n", opts.hookIdleTimeout, opts.hookTimeout)
		return nil, errUsage
	}
	if !opts.showVersion && len(opts.command) == 0 {
		fs.Usage()
		return nil, errUsage
	}
	return opts, nil
}

// attachOptions holds the flags for the attach subcommand: a pure proxy
// in front of a server the user runs themselves. There is no child, so no
// --internal-port; --upstream names the server to proxy to instead.
type attachOptions struct {
	listen       string
	upstream     string
	upstreamURL  *url.URL
	position     string
	size         string
	theme        string
	pills        []string
	open         bool
	quiet        bool
	allowHosts   []string
	unsafeListen bool
	keepCSP      bool
}

func parseAttachArgs(name string, args []string, out io.Writer) (*attachOptions, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(out)
	opts := &attachOptions{}
	fs.StringVar(&opts.listen, "listen", "127.0.0.1:3000", "address to listen on (loopback only unless --unsafe-listen)")
	fs.StringVar(&opts.upstream, "upstream", "", "upstream URL to proxy to, e.g. http://localhost:3100 (required, loopback only unless --unsafe-listen)")
	fs.StringVar(&opts.position, "position", knob.Default.Positions.Default, positionUsage)
	fs.StringVar(&opts.size, "size", knob.Default.Sizes.Default, sizeUsage)
	fs.StringVar(&opts.theme, "theme", knob.Default.Themes.Default, themeUsage)
	var pillsRaw string
	fs.StringVar(&pillsRaw, "pills", knob.Default.Pills.Default, pillsUsage)
	fs.BoolVar(&opts.open, "open", false, openUsage)
	fs.BoolVar(&opts.quiet, "quiet", false, "suppress marquee's informational output (warnings and errors still print)")
	fs.Var((*stringList)(&opts.allowHosts), "allow-host", "extra Host accepted on /__marquee/* endpoints; exact or *.suffix wildcard, e.g. *.lvh.me (repeatable)")
	fs.BoolVar(&opts.unsafeListen, "unsafe-listen", false, "allow a non-loopback --listen and --upstream, exposing the proxy to the network")
	fs.BoolVar(&opts.keepCSP, "keep-csp", false, "leave the app's Content-Security-Policy untouched (the bar may not load if its CSP forbids same-origin scripts)")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(out, "usage: marquee attach --upstream <url> [flags]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if positional := fs.Args(); len(positional) > 0 {
		_, _ = fmt.Fprintf(out, "marquee: attach takes no positional arguments (got %v); did you mean --upstream?\n", positional)
		return nil, errUsage
	}
	if !knob.Default.Positions.Valid(opts.position) {
		_, _ = fmt.Fprintf(out, "marquee: invalid --position %q: must be one of %s\n", opts.position, knob.Default.Positions.List())
		return nil, errUsage
	}
	if !knob.Default.Sizes.Valid(opts.size) {
		_, _ = fmt.Fprintf(out, "marquee: invalid --size %q: must be one of %s\n", opts.size, knob.Default.Sizes.List())
		return nil, errUsage
	}
	if !knob.Default.Themes.Valid(opts.theme) {
		_, _ = fmt.Fprintf(out, "marquee: invalid --theme %q: must be one of %s\n", opts.theme, knob.Default.Themes.List())
		return nil, errUsage
	}
	pills, err := parsePills(pillsRaw)
	if err != nil {
		_, _ = fmt.Fprintf(out, "marquee: %v\n", err)
		return nil, errUsage
	}
	opts.pills = pills
	u, err := parseUpstream(opts.upstream)
	if err != nil {
		_, _ = fmt.Fprintf(out, "marquee: %v\n", err)
		return nil, errUsage
	}
	opts.upstreamURL = u
	return opts, nil
}

// parseUpstream validates --upstream as a shape (present, parseable,
// http(s), has a host). Whether that host is loopback is a separate
// security check (validateUpstream) so it can carry its own exit code.
func parseUpstream(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, fmt.Errorf("--upstream is required (e.g. http://localhost:3100)")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid --upstream %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("invalid --upstream %q: scheme must be http or https", raw)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("invalid --upstream %q: missing host", raw)
	}
	return u, nil
}

// validateUpstream is the upstream twin of validateListen: a loopback
// upstream is always fine; a non-loopback one is refused unless unsafe is
// set, in which case it is allowed and unsafeAllowed is true so the caller
// can print the network-exposure warning. marquee is a localhost-only dev
// tool, so proxying to a remote host is refused by default.
func validateUpstream(u *url.URL, unsafe bool) (unsafeAllowed bool, err error) {
	if loopbackHost(u.Hostname()) {
		return false, nil
	}
	if !unsafe {
		return false, fmt.Errorf("refusing to proxy to non-loopback upstream %q — marquee only proxies to a server on your own machine; pass --unsafe-listen if you really mean to", u.Redacted())
	}
	return true, nil
}

// printUnsafeUpstreamWarning is the upstream twin of
// printUnsafeListenWarning: a persistent stderr banner, never routed
// through the logger, so --quiet cannot suppress it.
func printUnsafeUpstreamWarning(w io.Writer, upstream string) {
	const rule = "!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!"
	_, _ = fmt.Fprintf(w, "%s\n", rule)
	_, _ = fmt.Fprintf(w, "marquee: UNSAFE: proxying to non-loopback upstream %s\n", upstream)
	_, _ = fmt.Fprintf(w, "marquee: marquee is a localhost-only dev tool; sending traffic to a remote host is not what it is for.\n")
	_, _ = fmt.Fprintf(w, "%s\n", rule)
}

// validateListen enforces the loopback-only default and its escape hatch.
// A loopback address is always fine. A non-loopback address is refused
// unless unsafe is set, in which case it is allowed and unsafeAllowed is
// true so the caller can print the network-exposure warning.
func validateListen(listen string, unsafe bool) (unsafeAllowed bool, err error) {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false, fmt.Errorf("invalid --listen address %q: %w", listen, err)
	}
	if loopbackHost(host) {
		return false, nil
	}
	if !unsafe {
		return false, fmt.Errorf("refusing to listen on non-loopback address %q — this would expose the proxy and your dev app to the network; pass --unsafe-listen if you really mean to", listen)
	}
	return true, nil
}

// printUnsafeListenWarning writes a persistent, hard-to-miss banner about
// network exposure. It writes straight to w, never through the logger, so
// --quiet cannot suppress it.
func printUnsafeListenWarning(w io.Writer, listen string) {
	const rule = "!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!"
	_, _ = fmt.Fprintf(w, "%s\n", rule)
	_, _ = fmt.Fprintf(w, "marquee: UNSAFE: listening on non-loopback %s\n", listen)
	_, _ = fmt.Fprintf(w, "marquee: the proxy AND your dev app are now reachable by anyone on the network.\n")
	_, _ = fmt.Fprintf(w, "marquee: there is no auth in front of your app. do not use on an untrusted network.\n")
	_, _ = fmt.Fprintf(w, "%s\n", rule)
}

// browserURL turns a listen address into a URL safe to hand a browser.
// An unspecified host (0.0.0.0, ::, or empty) means "all interfaces" and is
// not a routable address in a browser, so it collapses to loopback; the port
// is preserved.
func browserURL(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "http://" + listen
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// browserCommand builds the platform command that opens url in the
// default browser: macOS uses open, everything else xdg-open.
func browserCommand(goos, url string) *exec.Cmd {
	// #nosec G204 -- the binary is a fixed per-OS literal and url is marquee's own
	// listen address (operator-supplied via --listen), never HTTP-derived input.
	if goos == "darwin" {
		return exec.Command("open", url)
	}
	// #nosec G204 -- see above: fixed binary, operator-supplied url.
	return exec.Command("xdg-open", url)
}

// openBrowser opens url once. Failure is never fatal (fail-open): if the
// opener is missing or errors, it logs a single warning and returns so
// marquee keeps serving.
func openBrowser(url string, log *logger) {
	if err := browserCommand(runtime.GOOS, url).Run(); err != nil {
		log.Warn("could not open a browser automatically (%v); open %s yourself", err, url)
	}
}
