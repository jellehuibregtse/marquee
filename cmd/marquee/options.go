package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
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
	fs.Var((*stringList)(&opts.allowHosts), "allow-host", "extra Host accepted on /__marquee/* endpoints (repeatable)")
	fs.BoolVar(&opts.unsafeListen, "unsafe-listen", false, "allow a non-loopback --listen, exposing the proxy to the network")
	fs.BoolVar(&opts.showVersion, "version", false, "print version and exit")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(out, "usage: marquee [flags] -- command [args...]")
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
