package main

import (
	"context"
	"errors"
	"flag"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jellehuibregtse/marquee/internal/ghinfo"
	"github.com/jellehuibregtse/marquee/internal/gitinfo"
	"github.com/jellehuibregtse/marquee/internal/proxy"
	"github.com/jellehuibregtse/marquee/internal/status"
)

// runAttach implements the attach subcommand: a pure reverse proxy in
// front of a server the user runs themselves. There is no child process,
// so none of the wrapper-mode machinery (runner, pidfile, PORT env, free
// port) applies; the git/gh pollers still run against the working
// directory so the bar reports branch/PR/worktree as usual.
func runAttach(args []string) int {
	opts, err := parseAttachArgs("marquee attach", args, os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	log := newLogger(os.Stderr, opts.quiet)

	workdir, err := os.Getwd()
	if err != nil {
		log.Error("could not determine working directory: %v", err)
		return 1
	}

	unsafeListen, err := validateListen(opts.listen, opts.unsafeListen)
	if err != nil {
		log.Error("%v", err)
		return 1
	}
	unsafeUpstream, err := validateUpstream(opts.upstreamURL, opts.unsafeListen)
	if err != nil {
		log.Error("%v", err)
		return 1
	}

	ln, err := net.Listen("tcp", opts.listen)
	if err != nil {
		log.Error("%s", listenErrorMessage(opts.listen, err))
		return 1
	}
	if unsafeListen {
		printUnsafeListenWarning(os.Stderr, opts.listen)
	}
	if unsafeUpstream {
		printUnsafeUpstreamWarning(os.Stderr, opts.upstreamURL.Redacted())
	}

	git := gitinfo.Start(workdir, 2*time.Second, nil)
	defer git.Stop()
	gh := ghinfo.New(workdir)
	defer gh.Stop()

	handler := proxy.New(proxy.Config{UpstreamURL: opts.upstreamURL, AllowHosts: opts.allowHosts, RelaxCSP: !opts.keepCSP})
	status.Register(handler.Internal(), status.Deps{
		Git:        git.Snapshot,
		PR:         gh.PR,
		ChildState: func() string { return "attached" },
		Position:   opts.position,
		Size:       opts.size,
		Theme:      opts.theme,
		Pills:      opts.pills,
	})

	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	log.Info("listening on http://%s, upstream %s", ln.Addr(), opts.upstreamURL.Redacted())

	if !opts.noOpen {
		go openUpstreamWhenHealthy(upstreamDialAddr(opts.upstreamURL), browserURL(opts.listen), log)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		log.Info("received %s, shutting down", sig)
		ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		defer cancel()
		_ = srv.Shutdown(ctx)
		return 0
	case err := <-serveErr:
		log.Error("server error: %v", err)
		return 1
	}
}

// upstreamDialAddr is the host:port openUpstreamWhenHealthy dials,
// mirroring the proxy's own probe address (scheme default port when the
// URL omits one).
func upstreamDialAddr(u *url.URL) string {
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// openUpstreamWhenHealthy waits for the upstream to accept connections and
// then opens url in the browser exactly once. Unlike wrapper mode there is
// no child to bound the wait, so it gives up after a generous deadline
// rather than spinning forever. It is fail-open: a browser that never
// opens is a warning, never a fatal error.
func openUpstreamWhenHealthy(upstreamAddr, url string, log *logger) {
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", upstreamAddr, 250*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			log.Info("upstream is up, opening %s", url)
			openBrowser(url, log)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	log.Warn("upstream %s never became reachable; not opening a browser", upstreamAddr)
}
