package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jellehuibregtse/marquee/internal/ghinfo"
	"github.com/jellehuibregtse/marquee/internal/gitinfo"
	"github.com/jellehuibregtse/marquee/internal/proxy"
	"github.com/jellehuibregtse/marquee/internal/runner"
	"github.com/jellehuibregtse/marquee/internal/status"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

const stopTimeout = 10 * time.Second

func main() {
	if len(os.Args) > 1 && os.Args[1] == "attach" {
		os.Exit(runAttach(os.Args[2:]))
	}
	os.Exit(run())
}

func run() int {
	opts, err := parseArgs(os.Args[0], os.Args[1:], os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	log := newLogger(os.Stderr, opts.quiet)

	if opts.showVersion {
		fmt.Printf("marquee %s (commit %s, built %s)\n", version, commit, date)
		return 0
	}

	workdir, err := os.Getwd()
	if err != nil {
		log.Error("could not determine working directory: %v", err)
		return 1
	}

	unsafeAllowed, err := validateListen(opts.listen, opts.unsafeListen)
	if err != nil {
		log.Error("%v", err)
		return 1
	}

	port := opts.internalPort
	if port == 0 {
		port, err = freePort()
		if err != nil {
			log.Error("could not pick a free internal port: %v", err)
			return 1
		}
	}

	ln, err := net.Listen("tcp", opts.listen)
	if err != nil {
		log.Error("%s", listenErrorMessage(opts.listen, err))
		return 1
	}
	if unsafeAllowed {
		printUnsafeListenWarning(os.Stderr, opts.listen)
	}

	pidPath, pidPathErr := pidfilePath(opts.listen)
	if pidPathErr == nil {
		warnStaleChild(pidPath, os.Stderr)
	}

	child := runner.New(opts.command, []string{
		fmt.Sprintf("PORT=%d", port),
		"MARQUEE=1",
		fmt.Sprintf("MARQUEE_PORT=%d", port),
	}, "")
	if err := child.Start(); err != nil {
		log.Error("could not start child: %v", err)
		_ = ln.Close()
		return 1
	}
	if pgid := child.PGID(); pidPathErr == nil && pgid > 0 {
		if err := writePidfile(pidPath, pgid); err != nil {
			log.Warn("could not write pidfile %s: %v", pidPath, err)
		} else {
			defer removePidfile(pidPath)
		}
	}

	git := gitinfo.Start(workdir, 2*time.Second, nil)
	defer git.Stop()
	gh := ghinfo.New(workdir)
	defer gh.Stop()

	handler := proxy.New(proxy.Config{InternalPort: port, AllowHosts: opts.allowHosts})
	status.Register(handler.Internal(), status.Deps{
		Git:        git.Snapshot,
		PR:         gh.PR,
		ChildState: func() string { return string(child.Status().State) },
		Position:   opts.position,
	})

	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	log.Info("listening on http://%s, upstream 127.0.0.1:%d, child: %s",
		ln.Addr(), port, strings.Join(opts.command, " "))

	if !opts.noOpen {
		go openWhenHealthy(child, fmt.Sprintf("127.0.0.1:%d", port), browserURL(opts.listen), log)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	childDone := make(chan struct{})
	go func() {
		for child.Status().State != runner.StateExited {
			time.Sleep(100 * time.Millisecond)
		}
		close(childDone)
	}()

	select {
	case sig := <-sigCh:
		log.Info("received %s, stopping child", sig)
		return stopChild(child, sig, log)
	case <-childDone:
		st := child.Status()
		code := exitCode(st.Err)
		log.Info("child exited (%s), shutting down", exitReason(st.Err))
		return code
	case err := <-serveErr:
		log.Error("server error: %v", err)
		if stopped := stopChild(child, syscall.SIGTERM, log); stopped != 0 {
			return stopped
		}
		return 1
	}
}

// openWhenHealthy waits for the child to start accepting connections on
// upstreamAddr and then opens url in the browser exactly once. If the
// child exits before it is ever healthy, no browser is opened — error
// paths never launch a browser.
func openWhenHealthy(child *runner.Runner, upstreamAddr, url string, log *logger) {
	for {
		if child.Status().State == runner.StateExited {
			return
		}
		conn, err := net.DialTimeout("tcp", upstreamAddr, 250*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			log.Info("app is up, opening %s", url)
			openBrowser(url, log)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func stopChild(child *runner.Runner, sig os.Signal, log *logger) int {
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	if err := child.Stop(ctx, sig); err != nil {
		log.Error("stopping child: %v", err)
		return 1
	}
	return 0
}

func loopbackHost(host string) bool {
	host = strings.ToLower(host)
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		return 0, err
	}
	return port, nil
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() >= 0 {
		return exit.ExitCode()
	}
	return 1
}

func exitReason(err error) string {
	if err == nil {
		return "status 0"
	}
	return err.Error()
}
