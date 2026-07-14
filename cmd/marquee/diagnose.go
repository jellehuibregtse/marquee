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

// portHolder finds the process listening on the TCP port via lsof and
// names it via ps. Both tools are optional and deadline-bound: a missing
// or hung tool reports ok=false (or a nameless PID) and startup
// diagnostics carry on without it.
func portHolder(port string) (pid int, name string, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), portLookupTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "lsof", "-ti", "tcp:"+port, "-sTCP:LISTEN").Output()
	if err != nil {
		return 0, "", false
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	pid, err = strconv.Atoi(strings.TrimSpace(first))
	if err != nil || pid <= 0 {
		return 0, "", false
	}
	name = "unknown"
	if psOut, psErr := exec.CommandContext(ctx, "ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output(); psErr == nil {
		if comm := strings.TrimSpace(string(psOut)); comm != "" {
			name = filepath.Base(comm)
		}
	}
	return pid, name, true
}
