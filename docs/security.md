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
- State-changing endpoints and their same-origin + token guards (no
  state-changing endpoint exists yet; the switch endpoint lands in v2).
