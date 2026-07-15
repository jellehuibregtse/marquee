//go:build darwin || linux

// Package runner manages the child process lifecycle: spawn in an own
// process group, signal forwarding, graceful stop with kill escalation,
// restart, and a TCP health check helper.
package runner

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// State describes the child process lifecycle phase.
type State string

const (
	// StateStarting means the child has not been spawned yet (initial
	// state, and the window between stop and respawn during a restart).
	StateStarting State = "starting"
	// StateRunning means the child process is alive.
	StateRunning State = "running"
	// StateExited means the child process has exited.
	StateExited State = "exited"
)

// Status is a snapshot of the child process state. Err is the exit error
// (nil for a clean exit) and only meaningful when State is StateExited.
type Status struct {
	State State
	Err   error
}

// Runner owns a single child process. The child runs in its own process
// group so that signals and kills reach the entire tree it spawns —
// marquee must never orphan a grandchild.
type Runner struct {
	argv     []string
	extraEnv []string

	mu           sync.Mutex
	dir          string
	cmd          *exec.Cmd
	state        State
	exitErr      error
	done         chan struct{}
	managedDepth int

	terminated chan struct{}
	termOnce   sync.Once
}

// New prepares a runner for argv, with extraEnv ("KEY=value" entries)
// merged over the parent environment, running in dir (empty means the
// parent's working directory). Stdio is inherited from the parent.
func New(argv []string, extraEnv []string, dir string) *Runner {
	return &Runner{
		argv:       argv,
		extraEnv:   extraEnv,
		dir:        dir,
		state:      StateStarting,
		terminated: make(chan struct{}),
	}
}

// Start spawns the child in its own process group.
func (r *Runner) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.argv) == 0 {
		return errors.New("runner: empty command")
	}
	if r.state == StateRunning {
		return errors.New("runner: already running")
	}
	// #nosec G204 -- argv is the command the operator passed on marquee's own command line; running it is the tool's core purpose and it is never influenced by HTTP input.
	cmd := exec.Command(r.argv[0], r.argv[1:]...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), r.extraEnv...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	r.cmd = cmd
	r.exitErr = nil
	r.state = StateRunning
	done := make(chan struct{})
	r.done = done
	go func() {
		err := cmd.Wait()
		r.mu.Lock()
		r.exitErr = err
		r.state = StateExited
		unmanaged := r.managedDepth == 0
		r.mu.Unlock()
		close(done)
		// A terminal exit fires Terminated only when the runner's lifecycle
		// is not being managed (a Stop, Restart, or a switcher-owned switch).
		// An exit that is part of a managed operation is the switcher's to
		// handle — main must not mistake it for the app dying on its own.
		if unmanaged {
			r.termOnce.Do(func() { close(r.terminated) })
		}
	}()
	return nil
}

// Terminated is closed once, on the first terminal child exit that happens
// while the runner's lifecycle is not being managed. Exits that are part of a
// Restart or a switcher-owned switch (see BeginManaged/EndManaged) never close
// it, so the shutdown path in main is switch-aware by construction: it treats
// a closed Terminated as "the app died on its own", never as the transient
// stop of a restart or the failure of a switch the switcher will revert.
func (r *Runner) Terminated() <-chan struct{} { return r.terminated }

// BeginManaged marks the start of a managed lifecycle window. While any such
// window is open, a child exit does not close Terminated — the caller owns the
// outcome. Windows nest (a Restart inside a switcher-managed switch), so it is
// paired with EndManaged via a depth counter.
func (r *Runner) BeginManaged() {
	r.mu.Lock()
	r.managedDepth++
	r.mu.Unlock()
}

// EndManaged closes a window opened by BeginManaged. It never retroactively
// fires Terminated: an exit that already happened inside the window stays the
// caller's to handle, which is what keeps marquee alive after a switch whose
// target and revert both fail to boot.
func (r *Runner) EndManaged() {
	r.mu.Lock()
	if r.managedDepth > 0 {
		r.managedDepth--
	}
	r.mu.Unlock()
}

// Signal delivers sig to the child's entire process group. Signaling a
// child that is not running is a no-op.
func (r *Runner) Signal(sig os.Signal) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != StateRunning {
		return nil
	}
	return r.signalGroupLocked(sig)
}

func (r *Runner) signalGroupLocked(sig os.Signal) error {
	s, ok := sig.(syscall.Signal)
	if !ok {
		return errors.New("runner: unsupported signal type")
	}
	err := syscall.Kill(-r.cmd.Process.Pid, s)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// Stop terminates the child gracefully: it sends sig (SIGTERM when nil)
// to the process group, waits until the child exits or ctx is done, and
// escalates to SIGKILL on the group if the wait expires. It returns once
// the child has exited; a child that was never started or has already
// exited is a no-op.
func (r *Runner) Stop(ctx context.Context, sig os.Signal) error {
	if sig == nil {
		sig = syscall.SIGTERM
	}
	r.mu.Lock()
	if r.state != StateRunning {
		r.mu.Unlock()
		return nil
	}
	done := r.done
	if err := r.signalGroupLocked(sig); err != nil {
		r.mu.Unlock()
		return err
	}
	r.mu.Unlock()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
	}

	r.mu.Lock()
	err := r.signalGroupLocked(syscall.SIGKILL)
	r.mu.Unlock()
	if err != nil {
		return err
	}
	<-done
	return nil
}

// Restart stops the child gracefully and spawns it again with the same
// command and environment. A non-empty dir becomes the new working
// directory — this is the hook the worktree switcher uses.
func (r *Runner) Restart(ctx context.Context, dir string) error {
	// The stop half of a restart is an expected, transient exit — never a
	// terminal one. The managed window makes sure the wait goroutine does not
	// close Terminated when the old child goes away.
	r.BeginManaged()
	defer r.EndManaged()
	if err := r.Stop(ctx, nil); err != nil {
		return err
	}
	r.mu.Lock()
	if dir != "" {
		r.dir = dir
	}
	r.state = StateStarting
	r.mu.Unlock()
	return r.Start()
}

// PGID reports the process-group id of the running child (the child
// leads its own group, so this equals its pid). It returns 0 when no
// child is running.
func (r *Runner) PGID() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != StateRunning {
		return 0
	}
	return r.cmd.Process.Pid
}

// Status reports the current child state.
func (r *Runner) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Status{State: r.state, Err: r.exitErr}
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
