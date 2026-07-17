package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"github.com/jellehuibregtse/marquee/internal/port"
	"github.com/jellehuibregtse/marquee/internal/proxy"
	"github.com/jellehuibregtse/marquee/internal/runner"
	"github.com/jellehuibregtse/marquee/internal/status"
	"github.com/jellehuibregtse/marquee/internal/switcher"
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

	internalPort := opts.internalPort
	if internalPort == 0 {
		internalPort, err = freePort()
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

	// On a restart (the switch path) the runner reclaims marquee's own internal
	// port before spawning the new child, so an escaped remnant of the old child
	// — a daemonizing process manager that survives the process-group stop —
	// cannot keep the port and make the new child fail to bind (or a stale
	// listener lie to the health probe). Scoped to this one loopback port; each
	// reap is logged.
	child := runner.New(opts.command, []string{
		fmt.Sprintf("PORT=%d", internalPort),
		"MARQUEE=1",
		fmt.Sprintf("MARQUEE_PORT=%d", internalPort),
	}, "", port.Reclaimer{
		Port: internalPort,
		Logf: func(format string, args ...any) { log.Info(format, args...) },
	})
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

	// The worktree switcher's CSRF token is minted once per process with
	// crypto/rand. If minting fails we run without a token: the switch
	// endpoint then rejects every request and the bar hides its switcher,
	// while everything else keeps working (fail-open).
	switchToken, err := mintToken()
	if err != nil {
		log.Warn("could not mint a switch token; the worktree switcher is disabled this run: %v", err)
		switchToken = ""
	}

	handler := proxy.New(proxy.Config{InternalPort: internalPort, AllowHosts: opts.allowHosts, RelaxCSP: !opts.keepCSP, SwitchToken: switchToken})
	status.Register(handler.Internal(), status.Deps{
		Git:        git.Snapshot,
		PR:         gh.PR,
		ChildState: func() string { return string(child.Status().State) },
		Position:   opts.position,
		Size:       opts.size,
		Theme:      opts.theme,
		Pills:      opts.pills,
	})
	if switchToken != "" {
		healthAddr := fmt.Sprintf("127.0.0.1:%d", internalPort)
		sw := switcher.New(switcher.Config{
			Token:      switchToken,
			Runner:     child,
			Collect:    gitinfo.Collect,
			Repoint:    func(dir string) { git.Repoint(dir); gh.Repoint(dir) },
			Health:     func(ctx context.Context) error { return port.WaitTCP(ctx, healthAddr, 0) },
			ChildAlive: func() bool { return child.Status().State == runner.StateRunning },
			Dir:        workdir,
			SwitchHook: opts.switchHook,
		})
		switcher.Register(handler.Internal(), sw)
		handler.SetSwitchSource(sw)
	}

	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	log.Info("listening on http://%s, upstream 127.0.0.1:%d, child: %s",
		ln.Addr(), internalPort, strings.Join(opts.command, " "))

	if !opts.noOpen {
		go openWhenHealthy(child, fmt.Sprintf("127.0.0.1:%d", internalPort), browserURL(opts.listen), log)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		log.Info("received %s, stopping child", sig)
		return stopChild(child, sig, log)
	case <-child.Terminated():
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

// mintToken returns a fresh 256-bit random token, hex-encoded, for the
// worktree switcher's CSRF guard. crypto/rand makes it unpredictable; hex
// keeps it free of HTML-special characters so it embeds into the injected
// snippet without escaping. It is generated once per process and never logged.
func mintToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
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
