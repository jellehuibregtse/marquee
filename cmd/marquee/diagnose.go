package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jellehuibregtse/marquee/internal/port"
)

const (
	occupiedPortHint  = "a dev server is probably already running there; stop it or pass --listen with another port."
	portLookupTimeout = 2 * time.Second
)

// listenErrorMessage turns a net.Listen failure into a friendly
// diagnostic. An address-in-use error names the PID and process holding
// the port when lsof can find them; every lookup failure degrades to the
// same message without the culprit.
func listenErrorMessage(addr string, err error) string {
	if !errors.Is(err, syscall.EADDRINUSE) {
		return fmt.Sprintf("marquee: could not listen on %s: %v", addr, err)
	}
	if _, port, splitErr := net.SplitHostPort(addr); splitErr == nil {
		if pid, name, ok := portHolder(port); ok {
			return fmt.Sprintf("marquee: %s is already in use by PID %d (%s) — %s", addr, pid, name, occupiedPortHint)
		}
	}
	return fmt.Sprintf("marquee: %s is already in use — %s", addr, occupiedPortHint)
}

// portHolder finds the process listening on the TCP port via port.Listeners
// and names it via ps. Both tools are optional and deadline-bound: a missing
// or hung tool reports ok=false (or a nameless PID) and startup
// diagnostics carry on without it.
func portHolder(portStr string) (pid int, name string, ok bool) {
	// --listen accepts service names as well as numbers (net.Listen resolves
	// "localhost:http"), so resolve the same way rather than only parsing digits.
	p, err := net.LookupPort("tcp", portStr)
	if err != nil || p <= 0 {
		return 0, "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), portLookupTimeout)
	defer cancel()
	pids, err := port.Listeners(ctx, p)
	if err != nil || len(pids) == 0 {
		return 0, "", false
	}
	pid = pids[0]
	name = "unknown"
	// #nosec G204 -- pid is an integer parsed from lsof output, and ps runs with a fixed argv; no value here originates from HTTP input.
	if psOut, psErr := exec.CommandContext(ctx, "ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output(); psErr == nil {
		if comm := strings.TrimSpace(string(psOut)); comm != "" {
			name = filepath.Base(comm)
		}
	}
	return pid, name, true
}
