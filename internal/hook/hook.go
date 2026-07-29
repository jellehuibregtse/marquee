// Package hook runs the operator's --switch-hook: the command that bootstraps a
// worktree before marquee starts the child there. Every child start goes through
// the same unit, whether it is the initial start in the worktree marquee was
// launched in or a start the switch orchestrator drives, so the exec details
// (own process group, group kill on timeout, prefixed log streaming) live in one
// place and the contract is identical on every leg.
package hook

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// DefaultTimeout bounds a hook run when the caller does not pick a timeout.
// Bootstrapping a worktree installs dependencies and clones databases, so the
// budget is generous.
const DefaultTimeout = 5 * time.Minute

// Config wires a Runner to its command and its two output sinks.
type Config struct {
	// Command is the operator's --switch-hook value. Empty makes the Runner a
	// working no-op.
	Command string
	// Timeout bounds one run; zero means DefaultTimeout.
	Timeout time.Duration
	// Logf receives the hook's output while it runs, so the operator watches a
	// bootstrap happen. It is ordinary informational output, which marquee's
	// --quiet suppresses.
	Logf func(string, ...any)
	// Errf receives the tail of a failing hook's output. A failure is a
	// diagnostic, not progress, so it must reach a sink --quiet cannot suppress;
	// otherwise a quiet run reports that the hook failed and never why. Nil falls
	// back to Logf.
	Errf func(string, ...any)
}

// Runner runs one operator-supplied command in a worktree. A Runner with an
// empty command is a working no-op, so callers never guard the call site.
type Runner struct {
	command string
	timeout time.Duration
	logf    func(string, ...any)
	errf    func(string, ...any)
}

// New builds a Runner from cfg.
func New(cfg Config) *Runner {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	errf := cfg.Errf
	if errf == nil {
		errf = logf
	}
	return &Runner{command: cfg.Command, timeout: timeout, logf: logf, errf: errf}
}

// Configured reports whether an operator actually supplied a hook command, so a
// caller can skip work that only exists to feed the hook.
func (r *Runner) Configured() bool { return r != nil && r.command != "" }

// Run runs the hook with its working directory set to dir. The hook's stdout and
// stderr stream to Logf, line by line and prefixed, so the operator sees a
// bootstrap happen. A non-zero exit or a timeout is returned as an error, and the
// tail of the output is repeated through Errf so the reason survives a sink
// --quiet drops; the caller decides what a failed bootstrap means for the child it
// was about to start.
func (r *Runner) Run(ctx context.Context, dir string) error {
	if !r.Configured() {
		return nil
	}
	r.logf("switch-hook: running %q in %s", r.command, dir)

	hctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	// #nosec G204 -- command is the operator's own CLI flag value (like the
	// wrapped dev command itself), never derived from the HTTP request or the
	// slug; dir is git's own worktree path, not request input. Running it via
	// "sh -c" is deliberate so operators can write pipelines and && chains.
	cmd := exec.CommandContext(hctx, "sh", "-c", r.command)
	cmd.Dir = dir
	// Run the hook in its own process group and kill the whole group on timeout,
	// so a hook like "bundle install" doesn't leak its children (ruby, native
	// builds) when it hangs — mirroring how the runner reaps the child. WaitDelay
	// bounds how long we wait for I/O to drain after.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	out := &output{logf: r.logf}
	cmd.Stdout = out
	cmd.Stderr = out
	err := cmd.Run()
	out.flush()
	if err != nil {
		// Repeat what the hook said on its way out. The streamed copy above went to
		// the informational sink, which --quiet drops; a failure has to explain
		// itself even then.
		for _, line := range out.tail {
			r.errf("switch-hook: %s", line)
		}
		return fmt.Errorf("switch-hook %q failed: %w", r.command, err)
	}
	return nil
}

// The tail kept for a failing hook is bounded on both axes, so a chatty hook
// (a "bundle install" resolving a hundred gems) cannot grow marquee's memory and
// cannot bury the actual error under its own progress either.
const (
	maxTailLines = 50
	maxTailBytes = 8 << 10
)

// output forwards the hook's combined output to the logger one line at a time,
// each line prefixed so hook progress is distinguishable in marquee's stderr,
// and keeps the last few lines so a failure can repeat them at error level.
// os/exec guarantees no concurrent Write when the same writer is used for both
// Stdout and Stderr, so no lock is needed.
type output struct {
	logf func(string, ...any)
	buf  []byte

	tail      []string
	tailBytes int
}

func (o *output) Write(p []byte) (int, error) {
	o.buf = append(o.buf, p...)
	for {
		i := bytes.IndexByte(o.buf, '\n')
		if i < 0 {
			break
		}
		o.line(string(o.buf[:i]))
		o.buf = o.buf[i+1:]
	}
	return len(p), nil
}

func (o *output) flush() {
	if len(o.buf) > 0 {
		o.line(string(o.buf))
		o.buf = nil
	}
}

func (o *output) line(s string) {
	o.logf("switch-hook: %s", s)
	if len(s) > maxTailBytes {
		s = s[:maxTailBytes]
	}
	o.tail = append(o.tail, s)
	o.tailBytes += len(s)
	for len(o.tail) > maxTailLines || (o.tailBytes > maxTailBytes && len(o.tail) > 1) {
		o.tailBytes -= len(o.tail[0])
		o.tail = o.tail[1:]
	}
}
