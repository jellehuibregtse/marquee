# Security

marquee terminates all browser traffic to the wrapped app, so its guards
are structural, not optional. This document tracks the mitigations as
implemented; the full threat model write-up is a separate task and will
extend this file.

## Implemented guards

### Loopback-only listener (LAN peers)

`marquee` refuses to start when `--listen` is not a loopback address
(`localhost`, `*.localhost`, or an IP for which `net.IP.IsLoopback` is
true). This keeps the proxy — and through it the dev app, which has no
auth of its own — unreachable from LAN peers.

The escape hatch is `--unsafe-listen`. A non-loopback `--listen` is a
hard refusal (exit 1, error naming the flag) unless `--unsafe-listen` is
also passed. When it is, marquee starts but prints a persistent,
unmissable banner at startup warning that the proxy and dev app are now
exposed to the network. That banner goes straight to stderr, never
through the info logger, so `--quiet` cannot suppress it. Use it only on
a trusted network, and never as a substitute for a real reverse proxy
with auth.

- Code: `loopbackHost`, `validateListen`, `printUnsafeListenWarning` in
  `cmd/marquee/{main.go,options.go}`
- Tests: `TestLoopbackHost` in `cmd/marquee/main_test.go`;
  `TestValidateListenLoopback`,
  `TestValidateListenNonLoopbackRefusedWithoutFlag`,
  `TestValidateListenNonLoopbackAllowedWithFlag`,
  `TestValidateListenInvalidAddress`, `TestUnsafeListenWarningIsLoud` in
  `cmd/marquee/options_test.go`

### Guarded internal mux (`/__marquee/`)

The `/__marquee/` namespace is never proxied to the upstream. All
handlers register through `proxy.InternalMux`, which enforces on every
request, before any handler runs:

- **Host allowlist** (DNS-rebinding defense): the request `Host` (port
  stripped, case-insensitive) must be `localhost`, `127.0.0.1`, `::1`,
  `*.localhost`, `*.lvh.me`, or an operator-supplied extra. Anything
  else gets a 403. Extras come from the repeatable `--allow-host` flag,
  which appends exact-match hosts to the allowlist for operators whose
  local dev domain is not covered by the built-ins. It only widens
  which `Host` values reach the read-only `/__marquee/*` endpoints; it
  never affects proxied app traffic (whose `Host` is untouched anyway)
  and grants no new capabilities.
- **`Cache-Control: no-store`** on every response.

There is no way to register an internal endpoint outside the guard: the
underlying `http.ServeMux` is unexported and only reachable via
`InternalMux.Handle`/`HandleFunc`.

- Code: `internal/proxy/internal.go`; the `--allow-host` flag is parsed
  in `cmd/marquee/options.go` and passed to `proxy.Config.AllowHosts`
  in `cmd/marquee/main.go`
- Tests: `TestInternalHostGuard` (allowlist mechanism, including an
  operator extra), `TestInternalNamespaceNeverProxied`,
  `TestInternalMuxRegistrationGoesThroughGuard` in
  `internal/proxy/proxy_test.go`; `TestAllowHostFlagReachesGuard` and
  `TestParseArgsAllowHostRepeatable` in `cmd/marquee/` prove the flag
  reaches the guard end to end

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

## Supply chain

The binary people install must be what CI built from reviewed source,
so the toolchain is kept small and continuously scanned.

- **Zero runtime dependencies.** `go.mod` requires nothing beyond the
  standard library, which keeps the dependency attack surface at zero.
- **`govulncheck` in CI.** Every push and pull request runs
  `govulncheck ./...` (pinned to `v1.6.0`) against the standard library
  and any future dependency, failing the build on a known, reachable
  vulnerability.
- **`gosec` in CI.** The `gosec` security linter runs as part of
  `golangci-lint` (config: `.golangci.yml`, alongside `errorlint`);
  golangci-lint is pinned to `v2.12.2` in the workflow. Findings are
  triaged, not blanket-ignored: real issues are fixed and the few
  false positives carry a narrowly-scoped `#nosec` with a justification
  naming why the input cannot come from HTTP (the subprocess launches in
  `runner`, `gitinfo`, `ghinfo`, and the startup port diagnostics all
  run fixed or operator-supplied argv, never request data).
- **Actions pinned by commit SHA.** Every `uses:` in
  `.github/workflows/` is pinned to a full commit SHA with a version
  comment, so a moved tag cannot swap the action out from under us.
- **Dependabot** (`.github/dependabot.yml`) watches the
  `github-actions` and `gomod` ecosystems weekly and opens PRs to bump
  pins, which then go through the same CI and human review.

- Config: `.github/workflows/ci.yml`, `.golangci.yml`,
  `.github/dependabot.yml`

## Not yet implemented

- Same-origin + token guards for process-state-changing endpoints
  (none exists yet; the switch endpoint lands in v2). The bar toggle
  above deliberately opts out of these — see its section for why.
