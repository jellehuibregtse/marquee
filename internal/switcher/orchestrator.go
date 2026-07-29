package switcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jellehuibregtse/marquee/internal/gitinfo"
	"github.com/jellehuibregtse/marquee/internal/hook"
	"github.com/jellehuibregtse/marquee/internal/switching"
)

// ChildController is the process side of a switch: restart the child into a
// worktree, report whether it is alive, and stream its unexpected exits. It is
// implemented by the runner. It replaces the bare childAlive/Restart funcs and
// the BeginManaged/EndManaged bracket the switch used to reach around into the
// runner: a switch is really one collaboration with the process, so it is one
// port. The orchestrator owns termination knowledge that belongs to a switch —
// while a switch is in flight, a child exit is the orchestrator's to convert
// into a revert — by consuming Exits itself; the runner keeps only the
// knowledge that belongs to it (swallowing exits it caused).
type ChildController interface {
	Restart(ctx context.Context, dir string) error
	Alive() bool
	Exits() <-chan struct{}
}

// Worktrees is the git side of a switch: read git's own worktree set (a switch
// validates a slug against this, never the filesystem) and repoint the pollers
// after a successful switch so the bar reports the worktree it restarted into.
type Worktrees interface {
	Collect(dir string) (gitinfo.Snapshot, error)
	Repoint(dir string)
}

// ErrUnknownSlug is returned by Prepare when a slug does not resolve to exactly
// one worktree in git's live worktree set.
var ErrUnknownSlug = errors.New("slug is not a known worktree")

// The switch's timeout defaults. They live here, beside the orchestrator that
// falls back to them, so the CLI flags that override them cannot drift from the
// values a caller gets by leaving the config field zero. The hook's own default
// belongs to the hook (hook.DefaultTimeout), which owns that run.
const (
	// DefaultRestartTimeout bounds a single stop-and-spawn of the child.
	DefaultRestartTimeout = 30 * time.Second
	// DefaultHealthTimeout bounds the post-restart readiness gate.
	DefaultHealthTimeout = 30 * time.Second
)

// Outcome classifies how a switch ended, so the HTTP layer can render the exact
// status and body without knowing the switch's internal decisions.
type Outcome int

const (
	// OutcomeSuccess: the target came up healthy and is now the current worktree.
	OutcomeSuccess Outcome = iota
	// OutcomeHookFailedBeforeStart: the switch hook failed before the child was
	// stopped, so nothing moved — the child is left running where it was.
	OutcomeHookFailedBeforeStart
	// OutcomeReverted: the target did not come up, but the revert to the previous
	// worktree did; marquee continues as if the switch never happened.
	OutcomeReverted
	// OutcomeBothFailed: the target and the revert both failed to boot; the child
	// is down and marquee stays alive so the user can retry.
	OutcomeBothFailed
)

// Result is what Switch returns: the outcome and the target's identity. The
// HTTP handler maps it to a response.
type Result struct {
	Outcome Outcome
	Slug    string
	Path    string
	IsMain  bool
}

// Plan is a resolved, validated switch target produced by Prepare: the exact
// worktree the switch will restart into, whether it is main, and whether the
// current worktree is dirty. The HTTP layer applies the dirty-confirm guard to
// Dirty/IsMain; the orchestrator acts only on Target.
type Plan struct {
	Slug   string
	Path   string
	IsMain bool
	Dirty  bool
}

// worktree is a worktree the child can run in, named as git's worktree list
// names it. The slug travels with the path because the switch hook is told which
// worktree it is bootstrapping, on every leg.
type worktree struct {
	slug string
	dir  string
}

// Transition is one phase change with the timestamp it happened. The timeline
// of transitions for the last switch is the v2 performance-measurement hook.
type Transition struct {
	Phase switching.Phase
	At    time.Time
}

// OrchestratorConfig wires an Orchestrator to its two ports and its knobs.
type OrchestratorConfig struct {
	// Child restarts the child and reports its liveness and unexpected exits.
	Child ChildController
	// Worktrees reads git's worktree set and repoints the pollers.
	Worktrees Worktrees
	// Health blocks until the restarted child accepts connections or ctx is done.
	// Nil skips the readiness wait.
	Health func(ctx context.Context) error
	// Dir is the worktree the child starts in (marquee's launch cwd).
	Dir string
	// Slug names that worktree the way git's worktree list does, so the hook gets
	// the same slug on a revert to it as a switch into it would have passed.
	Slug string
	// Logger receives operational messages. Defaults to log.Default().
	Logger *log.Logger
	// RestartTimeout bounds a single restart; defaults to 30s.
	RestartTimeout time.Duration
	// HealthTimeout bounds the post-restart readiness wait; defaults to 30s.
	HealthTimeout time.Duration
	// Hook runs the operator's bootstrap command in a worktree (cwd = git's own
	// worktree path) before the child starts there. main owns it because the
	// initial child start needs the same hook before any orchestrator exists. A
	// nil Hook disables it. See docs/security.md, Threat 4.
	Hook *hook.Runner
	// ReadyCmd is the optional operator readiness command: an extra gate the
	// target must pass before a switch is called a success, retried until it exits
	// 0 or HealthTimeout expires. It runs after Health, in the target worktree,
	// through "sh -c". It covers what a TCP accept on the child's own port cannot,
	// such as a Vite dev server or a worker health endpoint on a fixed port that a
	// remnant of the previous stack may still be holding. Like the hook command it
	// is CLI input, never request- or slug-derived. Empty disables it. See
	// docs/security.md, Threat 4.
	ReadyCmd string
}

// readyRetryInterval is how long to wait between attempts of ReadyCmd. A stack
// coming up takes seconds, so half a second is frequent enough to not add
// noticeable latency to a switch and slow enough that a command spawning a shell
// (curl, pg_isready) is not run in a tight loop.
const readyRetryInterval = 500 * time.Millisecond

// Orchestrator owns a worktree switch end to end behind Switch: the restart
// into the target, the readiness gate (health probe plus child-liveness), the
// single revert leg (running the hook and restarting the previous worktree),
// the slug lifetime, and the phase. It also owns the child's termination
// knowledge that a switch needs: a monitor forwards the child's unexpected
// exits outward as Terminated, except while a switch is in flight (the switch
// converts them into its revert) or after a switch left the child intentionally
// down (both legs failed). main selects on Terminated, whose sole meaning is
// "the child really died and nobody is handling it."
type Orchestrator struct {
	child     ChildController
	worktrees Worktrees
	health    func(ctx context.Context) error
	logger    *log.Logger

	restartTimeout time.Duration
	healthTimeout  time.Duration
	hook           *hook.Runner
	readyCmd       string

	mu sync.Mutex
	// current is the worktree the child is running in, named as git names it. Both
	// halves travel together because the hook needs the slug of whatever worktree
	// it is bootstrapping, including on the revert leg.
	current worktree
	// switching is true for the duration of a Switch. While it is set, the
	// monitor never forwards an exit: the exit belongs to the switch.
	switching bool
	// expectDown is set when a switch ends with the child intentionally down
	// (both legs failed). While it is set, the monitor keeps ignoring exits, so a
	// user who does nothing after a failed switch is not shut down; it clears the
	// moment a later switch brings a child back up healthy.
	expectDown bool
	timeline   []Transition

	progress atomic.Value // switching.Progress

	terminated chan struct{}
	termOnce   sync.Once
}

// NewOrchestrator builds an Orchestrator and starts the monitor that turns the
// child's unexpected exits into Terminated. Both ports are hard preconditions:
// a nil one is a wiring bug in main, so fail deterministically at construction
// rather than panicking mid-switch (or in the monitor) under load.
func NewOrchestrator(cfg OrchestratorConfig) *Orchestrator {
	if cfg.Child == nil {
		panic("switcher: OrchestratorConfig.Child is required")
	}
	if cfg.Worktrees == nil {
		panic("switcher: OrchestratorConfig.Worktrees is required")
	}
	o := &Orchestrator{
		child:          cfg.Child,
		worktrees:      cfg.Worktrees,
		health:         cfg.Health,
		logger:         cfg.Logger,
		current:        worktree{slug: cfg.Slug, dir: cfg.Dir},
		restartTimeout: orDuration(cfg.RestartTimeout, DefaultRestartTimeout),
		healthTimeout:  orDuration(cfg.HealthTimeout, DefaultHealthTimeout),
		hook:           cfg.Hook,
		readyCmd:       cfg.ReadyCmd,
		terminated:     make(chan struct{}),
	}
	if o.logger == nil {
		o.logger = log.Default()
	}
	o.progress.Store(switching.Progress{Phase: switching.Idle})
	go o.monitor()
	return o
}

// Terminated is closed once, the first time the child dies unexpectedly and no
// switch is handling it. This is the single outward channel main selects on to
// shut marquee down.
func (o *Orchestrator) Terminated() <-chan struct{} { return o.terminated }

// Progress reports the in-flight switch's phase and target slug (empty when
// idle), satisfying proxy.SwitchSource.
func (o *Orchestrator) Progress() switching.Progress {
	p, _ := o.progress.Load().(switching.Progress)
	return p
}

// Timeline returns the phase transitions of the most recent switch, each with
// the time it happened — the cheap timing hook.
func (o *Orchestrator) Timeline() []Transition {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]Transition(nil), o.timeline...)
}

// monitor forwards the child's unexpected exits outward as Terminated, unless a
// switch owns the exit. It ignores an exit while a switch is in flight, while a
// failed switch has left the child intentionally down, and — as a guard against
// a stale buffered exit of a since-replaced child — while the current child is
// actually alive. What remains is exactly an unexpected death nobody handled.
func (o *Orchestrator) monitor() {
	for range o.child.Exits() {
		o.mu.Lock()
		ignore := o.switching || o.expectDown
		o.mu.Unlock()
		if ignore || o.child.Alive() {
			continue
		}
		o.termOnce.Do(func() { close(o.terminated) })
		return
	}
}

// Prepare resolves slug against git's live worktree set collected from the
// current worktree and reports whether the switch would need dirty
// confirmation. It never acts on the child; the HTTP layer applies its guards
// to the returned Plan before calling Switch.
func (o *Orchestrator) Prepare(slug string) (Plan, error) {
	o.mu.Lock()
	dir := o.current.dir
	o.mu.Unlock()

	snap, err := o.worktrees.Collect(dir)
	if err != nil {
		return Plan{}, fmt.Errorf("collect worktrees: %w", err)
	}
	// The slug is only ever an exact-match key into git's own worktree list; the
	// absolute path comes from git's output, never from the request. An unknown
	// slug or any traversal shape simply fails to match.
	target, ok := resolveWorktree(snap.Worktrees, slug)
	if !ok {
		return Plan{}, ErrUnknownSlug
	}
	targetIsMain := len(snap.Worktrees) > 0 && target.Path == snap.Worktrees[0].Path
	return Plan{Slug: target.Slug, Path: target.Path, IsMain: targetIsMain, Dirty: snap.Dirty}, nil
}

// Switch performs the switch to plan.Target end to end and returns its outcome.
// It is the single entry point that owns the restart, the readiness gate, the
// revert, the slug lifetime, and the phase. ctx bounds the whole switch; the
// HTTP handler passes a background context so a client disconnect never aborts a
// switch mid-flight and strands the child.
func (o *Orchestrator) Switch(ctx context.Context, plan Plan) Result {
	o.mu.Lock()
	prev := o.current
	o.switching = true
	o.timeline = o.timeline[:0]
	o.mu.Unlock()

	// The interstitial goes up now (matching the child staying up during the
	// hook), and comes down when the switch settles. On exit, a switch that left
	// the child down is recorded as expectDown so the monitor keeps ignoring its
	// death; a switch that left a healthy child clears it.
	o.setPhase(switching.Stopping, plan.Slug)
	defer func() {
		o.mu.Lock()
		o.expectDown = !o.child.Alive()
		o.switching = false
		o.mu.Unlock()
		o.setPhase(switching.Idle, "")
	}()

	result := Result{Slug: plan.Slug, Path: plan.Path, IsMain: plan.IsMain}

	// Bootstrap the target BEFORE the current child is stopped. The hook runs
	// while the old child is still up and untouched, so a hook failure here has
	// stopped nothing and there is nothing to recover: report the failure and
	// leave the healthy child exactly where it is. A revert restart at this point
	// would be pointless work that can itself race its own teardown into a stale
	// process-manager socket and leave the dev server dead after what was only a
	// harmless hook failure.
	target := worktree{slug: plan.Slug, dir: plan.Path}
	if err := o.hook.Run(ctx, hook.Invocation{
		Leg:        hook.LegSwitch,
		TargetSlug: target.slug,
		TargetDir:  target.dir,
		PrevDir:    prev.dir,
	}); err != nil {
		o.logf("switch to %q failed in switch-hook: %v; left the child running in %q untouched", plan.Slug, err, prev.dir)
		result.Outcome = OutcomeHookFailedBeforeStart
		return result
	}

	if err := o.switchInto(ctx, target, plan.Slug, true); err == nil {
		result.Outcome = OutcomeSuccess
		return result
	} else {
		o.logf("switch to %q failed: %v; reverting to %q", plan.Slug, err, prev.dir)
	}

	// The forward attempt failed only after it had already stopped the old child,
	// so recover by restarting the previous worktree. Revert exactly once.
	// Whether the revert comes up healthy or not, marquee stays alive (a
	// dead-but-alive marquee the user can retry beats one that vanished); the
	// response always reports the switch as a failure, never a fake success.
	o.setPhase(switching.Reverting, plan.Slug)
	if err := o.revertInto(ctx, prev, target); err != nil {
		o.logf("revert to %q also failed: %v", prev.dir, err)
		result.Outcome = OutcomeBothFailed
		return result
	}
	result.Outcome = OutcomeReverted
	return result
}

// switchInto restarts the child in tgt and waits for it to become healthy. On
// success it repoints the pollers and records tgt as the current worktree; on
// failure (the restart could not start, or the child never became healthy) it
// returns the error and leaves the current worktree untouched so a revert
// restores it. phaseSlug is what the interstitial shows, which on a revert is
// still the target the user asked for rather than the worktree being restored.
// readyGate says whether the operator's readiness command is part of "healthy"
// for this leg; see revertInto for why the revert leg turns it off. switchInto
// deliberately does NOT run the switch hook: bootstrapping happens once, before
// the child is stopped (see Switch), so a hook failure is handled there and never
// reaches this restart step.
func (o *Orchestrator) switchInto(ctx context.Context, tgt worktree, phaseSlug string, readyGate bool) error {
	rctx, cancel := context.WithTimeout(ctx, o.restartTimeout)
	defer cancel()
	if err := o.child.Restart(rctx, tgt.dir); err != nil {
		return err
	}
	o.setPhase(switching.Booting, phaseSlug)

	// Probing covers both readiness gates and is recorded once, so the timeline
	// stays a record of distinct stages rather than one entry per gate.
	if o.health != nil || (readyGate && o.readyCmd != "") {
		o.setPhase(switching.Probing, phaseSlug)
	}

	if o.health != nil {
		hctx, hcancel := context.WithTimeout(ctx, o.healthTimeout)
		err := o.health(hctx)
		hcancel()
		if err != nil {
			return err
		}
	}

	// The operator's readiness command is the second, wider gate, and it runs only
	// once the cheap TCP accept has passed so a target that never listens at all
	// fails without spawning anything.
	if readyGate {
		if err := o.waitReady(ctx, tgt.dir); err != nil {
			return err
		}
	}

	// A passing health probe is not proof the new child booted: with a
	// daemonizing process manager, the probe can connect to a STALE listener, an
	// escaped remnant of the OLD child still holding the internal port. Require
	// the child to be actually running, so a switch to a child that has exited is
	// a failure (and reverts) rather than a fake success that later shuts marquee
	// down. Asserting it last also catches a child that died while the readiness
	// command was still being retried.
	if !o.child.Alive() {
		return fmt.Errorf("child exited before becoming healthy in %s", tgt.dir)
	}

	o.mu.Lock()
	o.current = tgt
	o.mu.Unlock()
	o.worktrees.Repoint(tgt.dir)
	return nil
}

// revertInto brings the child back to the previous, already-working worktree
// after a failed forward switch. It runs the switch hook there first — unlike a
// plain switchInto. The forward attempt stopped the old child, and its process
// manager (overmind, tmux) may have left a stale socket (e.g. .overmind.sock)
// that makes a clean reboot in the previous worktree fail ("already running").
// An operator hook such as "rm -f .overmind.sock" clears that so the revert can
// actually come back up. Re-bootstrapping a worktree that already worked is a
// little wasteful, but that cost is minor next to leaving the dev server dead.
// The hook is operator CLI input, never request-derived (see docs/security.md,
// Threat 4), so running it on the revert widens no attack surface. A hook
// failure here is logged, not fatal: the previous worktree already booted once,
// so recovery still attempts the restart rather than stranding the user on a
// spurious hook error — if the restart then fails, the both-failed path reports
// it honestly.
//
// The readiness command is the mirror image: the revert is not gated on it. This
// leg only has to get the dev server back up, and the wider check is exactly the
// one likeliest to keep failing (a stale port holder, or a command the operator
// typed wrong), which would turn a reverted switch into a reported "the dev
// server is down" while the previous worktree is in fact running again.
func (o *Orchestrator) revertInto(ctx context.Context, prev, failed worktree) error {
	if err := o.hook.Run(ctx, hook.Invocation{
		Leg:        hook.LegRevert,
		TargetSlug: prev.slug,
		TargetDir:  prev.dir,
		PrevDir:    failed.dir,
	}); err != nil {
		o.logf("revert switch-hook in %q failed; restarting the previously-working worktree anyway: %v", prev.dir, err)
	}
	return o.switchInto(ctx, prev, failed.slug, false)
}

// setPhase records a phase transition (with its timestamp, for timing) and
// publishes the new Progress for the proxy. The store is cheap so it stays out
// of the switch's way.
func (o *Orchestrator) setPhase(p switching.Phase, slug string) {
	now := time.Now()
	o.mu.Lock()
	o.timeline = append(o.timeline, Transition{Phase: p, At: now})
	o.mu.Unlock()
	o.progress.Store(switching.Progress{Phase: p, Slug: slug, Since: now})
}

// waitReady runs the operator's readiness command in the target worktree until
// it exits 0 or the health timeout expires, and is a no-op when none is
// configured. It is the gate for everything a TCP accept on the child's own port
// cannot see: with a process manager the stack has other fixed ports (an asset
// server, a worker's health endpoint), and if a remnant of the previous stack
// still holds one of them the web port comes up and the probe passes on a
// half-dead stack. Retrying is the point — a stack comes up in stages, so the
// first attempts are expected to fail — which is also why the command's output is
// not streamed: it would fill the log with the same "connection refused" once
// per attempt. Only the last attempt's output is logged, when the gate gives up.
//
// The timeout is a budget of its own rather than a share of the one the TCP wait
// already used, so a slow-but-successful port wait cannot leave the readiness
// command no time to pass.
func (o *Orchestrator) waitReady(ctx context.Context, dir string) error {
	if o.readyCmd == "" {
		return nil
	}
	o.logf("ready-cmd: waiting for %q in %s", o.readyCmd, dir)

	rctx, cancel := context.WithTimeout(ctx, o.healthTimeout)
	defer cancel()

	attempts := 0
	for {
		attempts++
		out, err := o.runReadyCmd(rctx, dir)
		if err == nil {
			o.logf("ready-cmd: passed after %d attempt(s)", attempts)
			return nil
		}
		select {
		case <-rctx.Done():
			o.logReadyOutput(out)
			return fmt.Errorf("ready-cmd %q did not pass within %s (%d attempts, last: %w)",
				o.readyCmd, o.healthTimeout, attempts, err)
		case <-time.After(readyRetryInterval):
		}
	}
}

// runReadyCmd runs the readiness command once and returns its combined output
// alongside the run error. The output is capped: the command is retried, so a
// chatty failure must not grow marquee's memory attempt after attempt.
func (o *Orchestrator) runReadyCmd(ctx context.Context, dir string) ([]byte, error) {
	cmd := o.operatorCommand(ctx, o.readyCmd, dir)
	out := &capBuffer{max: 8 << 10}
	cmd.Stdout = out
	cmd.Stderr = out
	err := cmd.Run()
	return out.buf.Bytes(), err
}

// logReadyOutput repeats the last attempt's output, one line at a time and
// prefixed, when the gate gives up.
func (o *Orchestrator) logReadyOutput(out []byte) {
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line != "" {
			o.logf("ready-cmd: %s", line)
		}
	}
}

// operatorCommand builds an "sh -c" command for an operator-supplied script, run
// with its cwd set to a worktree.
func (o *Orchestrator) operatorCommand(ctx context.Context, script, dir string) *exec.Cmd {
	// #nosec G204 -- script is the operator's own --ready-cmd CLI flag value,
	// exactly like the wrapped dev command itself, and is never derived from the
	// HTTP request or the switch slug; dir is git's own worktree path, not request
	// input. The "sh -c" form is deliberate so operators can write pipelines and
	// && chains.
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Dir = dir
	// Run it in its own process group and kill the whole group on timeout, so a
	// script that spawns children doesn't leak them when it hangs, mirroring how
	// the runner reaps the child and how the switch hook is run. WaitDelay bounds
	// how long we wait for I/O to drain after.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

func (o *Orchestrator) logf(format string, args ...any) {
	o.logger.Printf("marquee: "+format, args...)
}

// capBuffer collects at most max bytes and silently drops the rest, so capturing
// a retried command's output cannot grow without bound.
type capBuffer struct {
	buf bytes.Buffer
	max int
}

func (c *capBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if room := c.max - c.buf.Len(); room > 0 {
		if n > room {
			p = p[:room]
		}
		c.buf.Write(p)
	}
	// Report the whole write as accepted; a short count would abort the copy the
	// command's output is being drained through.
	return n, nil
}

func orDuration(d, fallback time.Duration) time.Duration {
	if d <= 0 {
		return fallback
	}
	return d
}
