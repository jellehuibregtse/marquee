# marquee

A CLI that wraps or attaches to a local dev server, reverse-proxies it, and injects a branch bar into its HTML so the developer always sees which worktree, branch, and PR they are looking at.

## Language

**Bar**:
The injected web component showing branch, worktree, and PR information on the proxied dev page.
_Avoid_: widget, toolbar, overlay

**Pill**:
One information element on the bar (branch, dirty, worktree, pr). The pills pref is an ordered subset: order is render order, omission hides.
_Avoid_: chip, badge

**Knob**:
A user-facing customization of the bar: position, size, theme, pills. Set via CLI flag, overridden per-browser in the settings panel.
_Avoid_: setting, option, preference

**Knob catalog**:
The single owner of every knob's ids, defaults, labels, and theme palettes. Both flag validation and the bar derive their tables from it.
_Avoid_: config schema, options table

**Child**:
The dev server process marquee spawns and owns in wrapper mode.
_Avoid_: subprocess, worker

**Upstream**:
The dev server marquee proxies but does not own, in attach mode.
_Avoid_: backend, origin

**Switch**:
The operation that repoints marquee at another worktree: restart the child there, gate on readiness, revert to the previous worktree on failure.
_Avoid_: checkout, worktree change

**Switch orchestrator**:
The module that owns a switch end to end — restart, readiness gate, revert, and phase — behind a single entry point.
_Avoid_: switcher handler, switch manager

**Phase**:
The observable stage of an in-flight switch, exposed by the switch orchestrator for the interstitial and for timing.
_Avoid_: state, step

**Unexpected termination**:
A child exit marquee did not cause itself. Only these are reported outward; exits marquee caused (stop, restart) are swallowed at the source.
_Avoid_: crash, managed window

**Interstitial**:
The placeholder page the proxy serves to browsers while the child is starting or a switch is in flight.
_Avoid_: splash, loading page
