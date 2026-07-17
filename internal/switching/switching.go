// Package switching is the leaf that lets the proxy observe an in-flight
// worktree switch without importing the switch orchestrator. It holds only the
// value types — the phase enum and a Progress snapshot — so both the
// orchestrator (which produces them) and the proxy (which reads them for the
// interstitial) can depend on it with no import cycle.
package switching

import "time"

// Phase is the observable stage of an in-flight switch. The orchestrator moves
// through these as it stops the child, boots it in the target worktree, probes
// readiness, and — on failure — reverts. Idle is the resting phase between
// switches.
type Phase string

const (
	// Idle: no switch is in flight.
	Idle Phase = "idle"
	// Stopping: the orchestrator is stopping the child to restart it elsewhere.
	Stopping Phase = "stopping"
	// Booting: the child has been (re)spawned in a worktree and is coming up.
	Booting Phase = "booting"
	// Probing: the readiness gate is waiting for the child to accept connections.
	Probing Phase = "probing"
	// Reverting: a switch failed and the child is being restored to the previous
	// worktree.
	Reverting Phase = "reverting"
)

// Progress is a snapshot of an in-flight switch: the current phase, the target
// worktree slug (empty when idle), and when the phase began. Since is the cheap
// per-transition timestamp the v2 performance-measurement hook reads; the proxy
// uses only Slug, to decide whether to serve the interstitial.
type Progress struct {
	Phase Phase
	Slug  string
	Since time.Time
}
