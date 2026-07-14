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
)

// errUsage signals a usage problem whose message parseArgs has already
// written; run turns it into exit code 2 without printing anything more.
var errUsage = errors.New("usage error")

type options struct {
	listen       string
	internalPort int
	position     string
	noOpen       bool
	quiet        bool
	allowHosts   []string
	unsafeListen bool
	keepCSP      bool
	showVersion  bool
	command      []string
}

// stringList collects a repeatable string flag (e.g. --allow-host a
// --allow-host b) into a slice in the order given.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func parseArgs(name string, args []string, out io.Writer) (*options, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(out)
	opts := &options{}
	fs.StringVar(&opts.listen, "listen", "127.0.0.1:3000", "address to listen on (loopback only unless --unsafe-listen)")
	fs.IntVar(&opts.internalPort, "internal-port", 0, "port the child binds to (0 picks a free port)")
	fs.StringVar(&opts.position, "position", "bottom", "where the bar renders: top or bottom")
	fs.BoolVar(&opts.noOpen, "no-open", false, "do not open the browser once the app is healthy")
	fs.BoolVar(&opts.quiet, "quiet", false, "suppress marquee's informational output (warnings and errors still print)")
	fs.Var((*stringList)(&opts.allowHosts), "allow-host", "extra Host accepted on /__marquee/* endpoints; exact or *.suffix wildcard, e.g. *.lvh.me (repeatable)")
	fs.BoolVar(&opts.unsafeListen, "unsafe-listen", false, "allow a non-loopback --listen, exposing the proxy to the network")
	fs.BoolVar(&opts.keepCSP, "keep-csp", false, "leave the app's Content-Security-Policy untouched (the bar may not load if its CSP forbids same-origin scripts)")
	fs.BoolVar(&opts.showVersion, "version", false, "print version and exit")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(out, "usage: marquee [flags] -- command [args...]")
		_, _ = fmt.Fprintln(out, "       marquee attach --upstream <url> [flags]   (proxy a server you run yourself)")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	opts.command = fs.Args()

	if opts.position != "top" && opts.position != "bottom" {
		_, _ = fmt.Fprintf(out, "marquee: invalid --position %q: must be top or bottom\n", opts.position)
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
	noOpen       bool
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
	fs.StringVar(&opts.position, "position", "bottom", "where the bar renders: top or bottom")
	fs.BoolVar(&opts.noOpen, "no-open", false, "do not open the browser once the upstream is healthy")
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
	if opts.position != "top" && opts.position != "bottom" {
		_, _ = fmt.Fprintf(out, "marquee: invalid --position %q: must be top or bottom\n", opts.position)
		return nil, errUsage
	}
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
