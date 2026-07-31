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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// The two bounds on a hook run. Both are backstops against a hook that is never
// going to finish. Neither one can tell whether the hook is making progress,
// because from outside the process there is nothing to read that off.
const (
	// DefaultIdleTimeout is how long a hook may produce no output at all before its
	// process group is killed. Silence implies neither progress nor its absence: a
	// bootstrap step that usually finishes in seconds has been seen to say nothing
	// for over two minutes, for reasons nobody could pin down afterwards, and the
	// first version of this bound killed a healthy dev stack over it. So this is not
	// a verdict on the step it interrupts. It exists only so a hook that will never
	// finish cannot hold the worktree forever, whether that is a stalled network
	// fetch, a lock nobody will release, or a prompt nobody will answer. Ten minutes
	// is generous enough that a stall has to look permanent before it fires, and
	// still well inside the hour the ceiling allows. No single number fits every
	// bootstrap, so a repo sets its own with --hook-idle-timeout.
	DefaultIdleTimeout = 10 * time.Minute
	// DefaultTimeout is the absolute ceiling on one run, used when the caller picks
	// none (--hook-timeout). It is deliberately far above any real bootstrap,
	// because a ceiling that fires on healthy work is worse than no ceiling at all:
	// a cold gem build with native extensions runs for many minutes and is not
	// stuck. The idle timeout is the bound that normally catches a wedged hook,
	// this one only catches a hook that keeps printing forever.
	DefaultTimeout = 60 * time.Minute
)

// Leg names which child start a hook run belongs to. One script has to serve
// all three, so the leg is exported to the hook as MARQUEE_HOOK_LEG and a hook
// that only cares about, say, cloning a database on a first-ever start can
// branch on it.
type Leg string

const (
	// LegStart is the initial child start, in the worktree marquee was launched in.
	LegStart Leg = "start"
	// LegSwitch is a switch into another worktree.
	LegSwitch Leg = "switch"
	// LegRevert is the fallback to the previous worktree after a failed switch.
	LegRevert Leg = "revert"
)

// Invocation is the worktree context of one hook run. Target is the worktree the
// child is about to start in, and the one the hook runs in; Prev is where the
// child was running until now, empty on LegStart because nothing was running
// yet.
type Invocation struct {
	Leg        Leg
	TargetSlug string
	TargetDir  string
	PrevDir    string
}

// env is the hook's view of the switch: the leg, the worktree it is bootstrapping
// and the one being left behind. These are passed as environment entries rather
// than substituted into the command, so nothing here can extend the operator's
// own shell command (see docs/security.md, Threat 4). The slug is git's, from the
// worktree list Prepare validated the request against.
func (inv Invocation) env() []string {
	return []string{
		"MARQUEE_HOOK_LEG=" + string(inv.Leg),
		"MARQUEE_TARGET_SLUG=" + inv.TargetSlug,
		"MARQUEE_TARGET_DIR=" + inv.TargetDir,
		"MARQUEE_PREV_DIR=" + inv.PrevDir,
	}
}

// Config wires a Runner to its command and its two output sinks.
type Config struct {
	// Command is the operator's --switch-hook value. Empty makes the Runner a
	// working no-op.
	Command string
	// Timeout is the absolute ceiling on one run; zero means DefaultTimeout.
	Timeout time.Duration
	// IdleTimeout is how long the run may produce no output before it is killed;
	// zero means DefaultIdleTimeout. It is not an operator knob — there is no flag
	// for it, because a hook that has gone quiet is hung whatever the operator
	// believes — so the only reason to set it is a test that cannot wait minutes.
	IdleTimeout time.Duration
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
	idle    time.Duration
	logf    func(string, ...any)
	errf    func(string, ...any)
}

// New builds a Runner from cfg.
func New(cfg Config) *Runner {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	idle := cfg.IdleTimeout
	if idle <= 0 {
		idle = DefaultIdleTimeout
	}
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	errf := cfg.Errf
	if errf == nil {
		errf = logf
	}
	return &Runner{command: cfg.Command, timeout: timeout, idle: idle, logf: logf, errf: errf}
}

// Configured reports whether an operator actually supplied a hook command, so a
// caller can skip work that only exists to feed the hook.
func (r *Runner) Configured() bool { return r != nil && r.command != "" }

// Run runs the hook in inv.TargetDir, with inv describing the switch in the
// hook's environment. The hook's stdout and stderr stream to Logf, line by line
// and prefixed, so the operator sees a bootstrap happen. A non-zero exit, a
// stretch of silence longer than the idle timeout, or the ceiling running out is
// returned as an error naming which of the three it was, and the tail of the
// output is repeated through Errf so the reason survives a sink --quiet drops;
// the caller decides what a failed bootstrap means for the child it was about to
// start.
func (r *Runner) Run(ctx context.Context, inv Invocation) error {
	if !r.Configured() {
		return nil
	}
	dir := inv.TargetDir
	r.logf("switch-hook: running %q in %s (%s)", r.command, dir, inv.Leg)

	ceiling, cancelCeiling := context.WithTimeout(ctx, r.timeout)
	defer cancelCeiling()
	hctx, cancel := context.WithCancel(ceiling)
	defer cancel()

	cmd := OperatorCommand(hctx, r.command, dir)
	// The switch reaches the hook through the environment, never through the
	// command text. Setting Env explicitly replaces the inherited copy, so the
	// parent's variables are re-added rather than assumed.
	cmd.Env = append(os.Environ(), inv.env()...)
	out := &output{logf: r.logf, activity: make(chan struct{}, 1)}
	cmd.Stdout = out
	cmd.Stderr = out

	idled := watchIdle(hctx, cancel, out.activity, r.idle)
	err := cmd.Run()
	out.flush()
	cancel()
	if err == nil {
		return nil
	}
	// Repeat what the hook said on its way out. The streamed copy above went to
	// the informational sink, which --quiet drops; a failure has to explain
	// itself even then.
	for _, line := range out.tail {
		r.errf("switch-hook: %s", line)
	}
	// The two kills read nothing alike to whoever has to act on them: silence
	// points at the step the hook was on, while the ceiling only says the whole
	// bootstrap was too long. Report which one happened.
	switch {
	case closed(idled):
		return fmt.Errorf("switch-hook %q produced no output for %s and was killed: %w", r.command, r.idle, err)
	case errors.Is(ceiling.Err(), context.DeadlineExceeded):
		return fmt.Errorf("switch-hook %q exceeded its %s limit and was killed: %w", r.command, r.timeout, err)
	}
	return fmt.Errorf("switch-hook %q failed: %w", r.command, err)
}

// watchIdle kills the run through cancel when nothing has been written for idle,
// and returns the channel it closes first so Run can tell an idle kill from any
// other failure. Every write to the hook's output is a sign of life, so the
// existing output writer is the whole liveness signal; the watchdog stops with
// the run's context.
func watchIdle(ctx context.Context, cancel context.CancelFunc, activity <-chan struct{}, idle time.Duration) <-chan struct{} {
	idled := make(chan struct{})
	go func() {
		timer := time.NewTimer(idle)
		defer timer.Stop()
		for {
			select {
			case <-activity:
				if !timer.Stop() {
					// The timer had already fired but the run is over anyway, or its send is
					// still queued; drain it so the reset starts from a clean timer.
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(idle)
			case <-timer.C:
				// Closed BEFORE the kill, so a Run that returns because of this kill
				// always observes it.
				close(idled)
				cancel()
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return idled
}

// closed reports whether ch has been closed, without blocking.
func closed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// OperatorCommand builds an "sh -c" command for a script the operator supplied
// on the command line, to run in a worktree. It is the one place marquee spawns
// operator script text: the switch hook goes through it, and so does the switch's
// readiness command (--ready-cmd), which needs the same cwd and the same
// process-group discipline but handles its own output. The caller sets the output
// sinks and, if the script should be told anything, cmd.Env.
//
// #nosec G204 -- script is an operator CLI flag value (--switch-hook or
// --ready-cmd), exactly like the wrapped dev command itself, and is never derived
// from the HTTP request or the switch slug; dir is git's own worktree path, not
// request input. The one request-touched value anywhere near a hook, the slug, is
// already an exact match against git's worktree list and reaches the script only
// as an environment entry, so it cannot extend the command. Running it via
// "sh -c" is deliberate so operators can write pipelines and && chains.
func OperatorCommand(ctx context.Context, script, dir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Dir = dir
	// Run the script in its own process group and kill the whole group when ctx
	// ends, so a script like "bundle install" doesn't leak its children (ruby,
	// native builds) when it hangs — mirroring how the runner reaps the child.
	// WaitDelay bounds how long we wait for I/O to drain after.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	return cmd
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
	// activity carries one non-blocking notification per write, for the idle
	// watchdog. Writes are the signal rather than whole lines, so a hook whose
	// progress is a carriage-returned counter with no newline in sight still counts
	// as alive.
	activity chan struct{}

	tail      []string
	tailBytes int
}

func (o *output) Write(p []byte) (int, error) {
	select {
	case o.activity <- struct{}{}:
	default:
	}
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
