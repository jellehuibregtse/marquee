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
	os.Exit(run())
}

func run() int {
	listen := flag.String("listen", "127.0.0.1:3000", "address to listen on (loopback only)")
	internalPort := flag.Int("internal-port", 0, "port the child binds to (0 picks a free port)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: marquee [--listen addr] [--internal-port port] -- command [args...]")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *showVersion {
		fmt.Printf("marquee %s (commit %s, built %s)\n", version, commit, date)
		return 0
	}

	command := flag.Args()
	if len(command) == 0 {
		flag.Usage()
		return 2
	}

	workdir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "marquee: could not determine working directory: %v\n", err)
		return 1
	}

	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		fmt.Fprintf(os.Stderr, "marquee: invalid --listen address %q: %v\n", *listen, err)
		return 1
	}
	if !loopbackHost(host) {
		fmt.Fprintf(os.Stderr, "marquee: refusing to listen on non-loopback address %q — marquee is a local dev tool and must not be exposed to the network\n", *listen)
		return 1
	}

	port := *internalPort
	if port == 0 {
		port, err = freePort()
		if err != nil {
			fmt.Fprintf(os.Stderr, "marquee: could not pick a free internal port: %v\n", err)
			return 1
		}
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, listenErrorMessage(*listen, err))
		return 1
	}

	pidPath, pidPathErr := pidfilePath(*listen)
	if pidPathErr == nil {
		warnStaleChild(pidPath, os.Stderr)
	}

	child := runner.New(command, []string{
		fmt.Sprintf("PORT=%d", port),
		"MARQUEE=1",
		fmt.Sprintf("MARQUEE_PORT=%d", port),
	}, "")
	if err := child.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "marquee: could not start child: %v\n", err)
		_ = ln.Close()
		return 1
	}
	if pgid := child.PGID(); pidPathErr == nil && pgid > 0 {
		if err := writePidfile(pidPath, pgid); err != nil {
			fmt.Fprintf(os.Stderr, "marquee: could not write pidfile %s: %v\n", pidPath, err)
		} else {
			defer removePidfile(pidPath)
		}
	}

	git := gitinfo.Start(workdir, 2*time.Second, nil)
	defer git.Stop()
	gh := ghinfo.New(workdir)
	defer gh.Stop()

	handler := proxy.New(proxy.Config{InternalPort: port})
	status.Register(handler.Internal(), status.Deps{
		Git:        git.Snapshot,
		PR:         gh.PR,
		ChildState: func() string { return string(child.Status().State) },
	})

	srv := &http.Server{Handler: handler}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	fmt.Fprintf(os.Stderr, "marquee: listening on http://%s, upstream 127.0.0.1:%d, child: %s\n",
		ln.Addr(), port, strings.Join(command, " "))

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
		fmt.Fprintf(os.Stderr, "marquee: received %s, stopping child\n", sig)
		return stopChild(child, sig)
	case <-childDone:
		st := child.Status()
		code := exitCode(st.Err)
		fmt.Fprintf(os.Stderr, "marquee: child exited (%s), shutting down\n", exitReason(st.Err))
		return code
	case err := <-serveErr:
		fmt.Fprintf(os.Stderr, "marquee: server error: %v\n", err)
		if stopped := stopChild(child, syscall.SIGTERM); stopped != 0 {
			return stopped
		}
		return 1
	}
}

func stopChild(child *runner.Runner, sig os.Signal) int {
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	if err := child.Stop(ctx, sig); err != nil {
		fmt.Fprintf(os.Stderr, "marquee: stopping child: %v\n", err)
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
