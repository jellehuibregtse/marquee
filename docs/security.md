# Security

marquee terminates all browser traffic to the wrapped app, so its guards
are structural, not optional. This document tracks the mitigations as
implemented; the full threat model write-up is a separate task and will
extend this file.

## Implemented guards

### Loopback-only listener

`marquee` refuses to start when `--listen` is not a loopback address
(`localhost`, `*.localhost`, or an IP for which `net.IP.IsLoopback` is
true). This keeps the proxy — and through it the dev app — unreachable
from LAN peers. There is no override flag yet.

- Code: `loopbackHost` in `cmd/marquee/main.go`
- Test: `TestLoopbackHost` in `cmd/marquee/main_test.go`

### Guarded internal mux (`/__marquee/`)

The `/__marquee/` namespace is never proxied to the upstream. All
handlers register through `proxy.InternalMux`, which enforces on every
request, before any handler runs:

- **Host allowlist** (DNS-rebinding defense): the request `Host` (port
  stripped, case-insensitive) must be `localhost`, `127.0.0.1`, `::1`,
  `*.localhost`, `*.lvh.me`, or an operator-supplied extra. Anything
  else gets a 403.
- **`Cache-Control: no-store`** on every response.

There is no way to register an internal endpoint outside the guard: the
underlying `http.ServeMux` is unexported and only reachable via
`InternalMux.Handle`/`HandleFunc`.

- Code: `internal/proxy/internal.go`
- Tests: `TestInternalHostGuard`, `TestInternalNamespaceNeverProxied`,
  `TestInternalMuxRegistrationGoesThroughGuard` in
  `internal/proxy/proxy_test.go`

Note the deliberate asymmetry: proxied app traffic keeps its `Host`
untouched (multi-tenant subdomain routing needs it); only marquee's own
endpoints validate `Host`.

### Internal endpoints (read-only)

`GET /__marquee/status` and `GET /__marquee/bar.js` are registered
through the guarded mux, so they inherit the Host allowlist and
`no-store` guards by construction. Both are GET-only (405 otherwise)
and change no state. The status payload contains repository metadata
only — branch, dirty flag, worktree paths, repo root, PR
number/title/URL, child state. No environment variable or token
values, no operator secrets, and no command lines appear in any
response or log line.

- Code: `internal/status/status.go`
- Tests: `TestHostGuardEnforcedThroughMux`, `TestStatusJSONShape`,
  `TestStatusMethodNotAllowed` in `internal/status/status_test.go`

### Bar toggle endpoint (`GET /__marquee/toggle`)

`GET /__marquee/toggle?bar=on|off` flips a process-wide boolean that
controls bar injection; without a parameter it reports the current
state, and an invalid parameter gets a 400 with a usage hint. It is
registered through the guarded mux, so it inherits the Host allowlist
and `no-store` guards, and it is GET-only (405 otherwise).

This endpoint mutates state, but deliberately does **not** carry the
stricter guards planned for process-state-changing endpoints
(same-origin proof plus a per-process token — the §3.5 rules the v2
switch endpoint will enforce). It is a GET on purpose: a human
mid-flow types it into the address bar. The only thing it can change
is whether the bar snippet is spliced into HTML responses — never
process state, never proxying — and it discloses only the bar state
plus, when the launch-time hard-off is active, the name of the
documented setting that caused it (`MARQUEE_DISABLE_BAR`; never any
environment variable value). The worst a hostile page could achieve
by triggering it cross-site is hiding or showing the dev bar, which
the operator flips back with one request;
that cosmetic blast radius is why the lighter guard set is acceptable
here and only here. `MARQUEE_DISABLE_BAR=1` at launch is a hard off
that the toggle cannot override. Anything that spawns, signals, or
selects a cwd keeps the full §3.5 rules.

- Code: `internal/proxy/bypass.go`
- Tests: `TestToggleGuardedByInternalMux`, `TestToggleMethodNotAllowed`,
  `TestToggleInvalidParamRejected`,
  `TestEnvDisablesInjectionAndToggleCannotReenable` in
  `internal/proxy/bypass_test.go`

The other two bypass switches have no endpoint surface: the
`X-Marquee: skip` request header is stripped before the request goes
upstream (the app never sees marquee plumbing —
`TestXMarqueeHeaderNeverForwardedUpstream`), and the
`MARQUEE_DISABLE_BAR` environment variable is read once at startup,
never at request time.

### Fail-open upstream errors

Upstream connection failures never surface as raw errors: browser
navigations get a self-refreshing "starting" page (503), other requests
a plain 503. No stack traces, no internal details in responses.

- Code: `internal/proxy/starting.go`
- Tests: `TestStartingPageWhileUpstreamNotReady`,
  `TestUpstreamDiesMidRunServesStartingPage`

## Not yet implemented

- `--unsafe-listen` escape hatch (non-loopback listening stays a hard
  refusal until then).
- Same-origin + token guards for process-state-changing endpoints
  (none exists yet; the switch endpoint lands in v2). The bar toggle
  above deliberately opts out of these — see its section for why.
