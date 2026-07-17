//go:build darwin || linux

// Package port owns loopback TCP port inspection and reclaim: finding the
// PIDs listening on a port (via lsof), freeing marquee's internal port from
// an escaped child remnant between the stop and spawn halves of a restart,
// and waiting for an address to accept connections.
package port

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	releaseGrace = 1500 * time.Millisecond
	releasePoll  = 50 * time.Millisecond
)

// Reclaimer binds Free to a specific loopback port and log sink so a runner can
// take it as a construction-time dependency instead of being configured by a
// setter it reads back on restart. Free's per-PID reap lines and this wrapper's
// failure line both go to Logf; a nil Logf discards them.
type Reclaimer struct {
	Port int
	Logf func(string, ...any)
}

// Free reclaims the bound port before the new child rebinds it. A failure is
// best-effort news — the new child simply fails to bind, which the switch
// orchestrator detects and reverts, so marquee never self-kills — but it is
// logged and returned for callers that want it.
func (r Reclaimer) Free(ctx context.Context) error {
	err := Free(ctx, r.Port, r.Logf)
	if err != nil && r.Logf != nil {
		r.Logf("switch: internal port %d not freed before restart: %v", r.Port, err)
	}
	return err
}

// Free ensures 127.0.0.1:port has no listener before a new child binds it. It
// first polls briefly for a just-stopped child to release the port on its own;
// only if it is still held past the grace window does it reap the listener(s)
// — an escaped remnant a process-group stop could not reach — and confirm the
// port is free. The scope is deliberately narrow: only this one loopback port,
// only the PIDs listening on it. logf, if non-nil, receives one line per
// reaped PID, naming the PID and that it held the internal port — never a
// command line or any secret.
func Free(ctx context.Context, port int, logf func(string, ...any)) error {
	if port <= 0 {
		return nil
	}
	if waitReleased(ctx, port) {
		return nil
	}
	// A Listeners failure (lsof missing or failing) yields no PIDs: the caller
	// falls back to letting the new child fail to bind — a failed switch the
	// switcher reverts — rather than killing anything.
	pids, _ := Listeners(ctx, port)
	for _, pid := range pids {
		reap(pid, port, logf)
	}
	if waitReleased(ctx, port) {
		return nil
	}
	return fmt.Errorf("port %d still held after reaping its listeners", port)
}

// Listeners returns the PIDs listening on the given loopback TCP port via
// lsof. lsof is optional and deadline-bound: a missing or failing tool is an
// error the caller degrades on — the reclaim path proceeds with no PIDs and
// the startup diagnostic drops the culprit's name.
func Listeners(ctx context.Context, port int) ([]int, error) {
	// #nosec G204 -- port is an integer loopback port marquee owns: the
	// internal port marquee itself chose and opened, or the port from the
	// operator-supplied --listen address. It is never derived from HTTP input,
	// and lsof runs with a fixed argv. Every PID acted on comes straight from
	// lsof's report of LISTEN sockets on that single port.
	out, err := exec.CommandContext(ctx, "lsof", "-ti", fmt.Sprintf("tcp:%d", port), "-sTCP:LISTEN").Output()
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if pid, err := strconv.Atoi(line); err == nil && pid > 1 {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

func waitReleased(ctx context.Context, port int) bool {
	deadline := time.Now().Add(releaseGrace)
	for {
		if !held(port) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(releasePoll):
		}
	}
}

// held reports whether something is currently listening on 127.0.0.1:port. It
// dials rather than binds, so it never grabs the port from the child that is
// about to claim it.
func held(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// reap terminates a single PID that holds marquee's internal port. pid 0 and 1
// are never signalled. An already-gone process (ESRCH) is a success.
func reap(pid, port int, logf func(string, ...any)) {
	if pid <= 1 {
		return
	}
	err := syscall.Kill(pid, syscall.SIGKILL)
	if err != nil && !errors.Is(err, syscall.ESRCH) {
		if logf != nil {
			logf("switch: could not reap PID %d holding internal port %d: %v", pid, port, err)
		}
		return
	}
	if logf != nil {
		logf("switch: reaped PID %d holding marquee's internal port %d (escaped child remnant)", pid, port)
	}
}

// WaitTCP polls addr ("host:port") until it accepts a TCP connection or
// ctx is done. A non-positive interval defaults to 50ms.
func WaitTCP(ctx context.Context, addr string, interval time.Duration) error {
	if interval <= 0 {
		interval = 50 * time.Millisecond
	}
	dialer := net.Dialer{Timeout: interval}
	for {
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err == nil {
			return conn.Close()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}
