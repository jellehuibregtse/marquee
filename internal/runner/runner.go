//go:build darwin || linux

// Package runner manages the child process lifecycle: spawn in an own
// process group, signal forwarding, graceful stop with kill escalation,
// restart, and a TCP health check helper.
package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
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

	mu       sync.Mutex
	dir      string
	cmd      *exec.Cmd
	state    State
	exitErr  error
	done     chan struct{}
	stopping bool

	reclaim PortReclaimer

	exits chan struct{}
}

// PortReclaimer frees marquee's internal loopback port of any escaped remnant
// of the old child before Restart spawns the new one. It is supplied at
// construction rather than set afterward, so the runner never holds the port
// and log sink as fields a later Restart reads back — the reclaim is a real
// dependency, not a bolt-on. A nil reclaimer disables the step (attach mode and
// most tests). internal/port supplies the production implementation; runner
// tests pass a fake, so the reclaim is testable without a real listener.
type PortReclaimer interface {
	Free(ctx context.Context) error
}

// New prepares a runner for argv, with extraEnv ("KEY=value" entries)
// merged over the parent environment, running in dir (empty means the
// parent's working directory). Stdio is inherited from the parent. reclaim,
// when non-nil, frees marquee's internal port on the restart path; pass nil to
// disable it.
func New(argv []string, extraEnv []string, dir string, reclaim PortReclaimer) *Runner {
	return &Runner{
		argv:     argv,
		extraEnv: extraEnv,
		dir:      dir,
		reclaim:  reclaim,
		state:    StateStarting,
		exits:    make(chan struct{}, 1),
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
		// An exit the runner caused itself — the stop half of a Stop or Restart —
		// is expected and swallowed at the source: it never reaches Exits. The
		// stopping flag is cleared here, under the same lock that set it, so the
		// next spawn's death is reported normally.
		expected := r.stopping
		r.stopping = false
		r.mu.Unlock()
		close(done)
		if !expected {
			r.emitExit()
		}
	}()
	return nil
}

// emitExit reports one unexpected child exit on the Exits channel without ever
// blocking the wait goroutine. The channel is buffered to one: a child can only
// die once per spawn, and the sole consumer (the switch orchestrator) drains it
// promptly, so a full buffer means an earlier unhandled exit is already pending
// and this one adds nothing.
func (r *Runner) emitExit() {
	select {
	case r.exits <- struct{}{}:
	default:
	}
}

// Exits reports the child's UNEXPECTED terminations — exits marquee did not
// cause itself. It receives one token per such exit and is never closed, so it
// re-arms across restarts: a child that fails to boot during a switch, and a
// later natural death of a healthy child, each send once. Exits the runner
// caused (the stop half of Stop or Restart) are swallowed at the source and
// never appear here. The switch orchestrator is the sole consumer: while a
// switch is in flight it owns these exits (converting them into the revert
// path); when idle it forwards them outward as the app dying on its own.
func (r *Runner) Exits() <-chan struct{} { return r.exits }

// Alive reports whether the child process is currently running. The switch
// orchestrator consults it right after the health probe, because a TCP probe
// can pass against a stale listener left by an escaped daemon even though the
// new child has already exited.
func (r *Runner) Alive() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state == StateRunning
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
	// Mark the coming exit as one the runner caused, so the wait goroutine
	// swallows it instead of reporting it on Exits.
	r.stopping = true
	if err := r.signalGroupLocked(sig); err != nil {
		r.stopping = false
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
	// The stop half of a restart is an expected, transient exit that Stop marks
	// as runner-caused, so it is swallowed and never reported on Exits. The new
	// child spawned below re-arms Exits: if it dies, that is reported normally,
	// leaving the switch orchestrator (not the runner) to decide whether it was
	// a switch failure or the app dying on its own.
	if err := r.Stop(ctx, nil); err != nil {
		return err
	}
	r.mu.Lock()
	if dir != "" {
		r.dir = dir
	}
	r.state = StateStarting
	r.mu.Unlock()
	// Between the stop and the spawn, reclaim marquee's own internal port from
	// any escaped remnant of the old child so the new one can bind. Best-effort:
	// if the port stays held, the new child simply fails to bind, which the
	// switcher detects as a failed switch and reverts — marquee never self-kills.
	if r.reclaim != nil {
		_ = r.reclaim.Free(ctx)
	}
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
