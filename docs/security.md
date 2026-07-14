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

#### Loopback-only upstream (attach mode)

`marquee attach --listen <addr> --upstream <url>` is a pure proxy in
front of a server the user runs themselves (no child process). Its
`--listen` obeys the same loopback-only rule as wrapper mode, and its
`--upstream` gets the symmetric guard: marquee refuses to proxy to a
non-loopback upstream (exit 1, error naming the flag), because it is a
localhost-only dev tool and has no business forwarding a browser's
traffic to a remote host. The escape hatch is the same `--unsafe-listen`
flag — passing it allows a non-loopback upstream (or listener) and
prints a persistent, unmissable stderr banner that `--quiet` cannot
suppress. A missing, empty, unparseable, or non-`http(s)` `--upstream`
is a usage error (exit 2) before any network action is taken.

The upstream check inspects only the host string (`loopbackHost`); a
non-loopback upstream is rejected without ever dialing it, so a refusal
performs no network action.

- Code: `runAttach`, `parseAttachArgs`, `parseUpstream`,
  `validateUpstream`, `printUnsafeUpstreamWarning` in
  `cmd/marquee/{attach.go,options.go}`; the explicit upstream flows into
  `proxy.Config.UpstreamURL` (`internal/proxy/proxy.go`), which drives
  both the reverse-proxy target and the liveness probe
- Tests: `TestValidateUpstreamLoopback`,
  `TestValidateUpstreamNonLoopbackRefused` (the abuse test: refused with
  no network action), `TestValidateUpstreamNonLoopbackAllowedWithFlag`,
  `TestUnsafeUpstreamWarningIsLoud`, `TestParseAttachArgsUpstreamRequired`,
  `TestParseAttachArgsBadScheme`, `TestAttachProxiesAndInjects` in
  `cmd/marquee/attach_test.go`;
  `TestConfigUpstreamURLOverridesInternalPort`,
  `TestConfigDefaultsToInternalPort` (wrapper-mode regression guard) in
  `internal/proxy/proxy_test.go`

### Guarded internal mux (`/__marquee/`)

The `/__marquee/` namespace is never proxied to the upstream. All
handlers register through `proxy.InternalMux`, which enforces on every
request, before any handler runs:

- **Host allowlist** (DNS-rebinding defense): the request `Host` (port
  stripped, case-insensitive) must be `localhost`, `127.0.0.1`, `::1`,
  `*.localhost`, or an operator-supplied extra. Anything else gets a
  403. `*.localhost` is built in because RFC 6761 reserves `.localhost`
  for loopback — it can never be registered by a third party, so
  trusting it by default is safe.

  `*.lvh.me` is **not** trusted by default. `lvh.me` is a third-party
  public wildcard domain (every subdomain resolves to `127.0.0.1`);
  trusting it by default would outsource the rebinding trust boundary
  to an external operator, so operators who rely on it must opt in
  explicitly with `--allow-host '*.lvh.me'`.

  Extras come from the repeatable `--allow-host` flag. Each value is
  either an exact host (e.g. `myapp.test`) or a `*.<suffix>` wildcard
  (e.g. `*.lvh.me`, also accepted as `.lvh.me`). A wildcard matches any
  subdomain, anchored on a dot boundary: `*.lvh.me` matches
  `app.lvh.me` but neither the bare apex `lvh.me` nor lookalikes
  like `evil-lvh.me` or `lvh.me.evil.com`. The flag only widens which
  `Host` values reach the read-only `/__marquee/*` endpoints; it never
  affects proxied app traffic (whose `Host` is untouched anyway) and
  grants no new capabilities.
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

The state-changing path (a valid `bar=on|off`) additionally rejects
clearly cross-origin requests. The Host allowlist is a DNS-rebinding
defense, not a CSRF one: any cross-site page can fire
`GET /__marquee/toggle?bar=off` (e.g. via `<img src>`) and a background
tab can re-issue it on an interval to persistently suppress the bar's
branch/dirty/PR safety indicator. To close that, the toggle consults
`Sec-Fetch-Site`, which browsers set and page JS cannot forge: a typed
address-bar navigation sends `none` and a same-origin fetch sends
`same-origin` (both **allowed**), while a cross-site or same-site
cross-origin page sends `cross-site`/`same-site` (**rejected with 403**,
`toggleOff` unchanged). The header is absent for `curl` and scripted
use, which stays **allowed** — its absence is a hardening signal, not a
gate, so the documented "type it in the address bar" and CLI uses never
break. The check applies only to the mutating path: a no-parameter state
report changes nothing and discloses only the bar state (plus, when the
launch-time hard-off is active, the name of the setting that caused it —
`MARQUEE_DISABLE_BAR`, never any environment variable value), so it stays
open even cross-site; an invalid value is a 400 before any origin check.

This is still the lighter guard set relative to process-state-changing
endpoints (which additionally require a per-process token — the §3.5 rules
the v2 switch endpoint will enforce): the toggle only decides whether the
bar snippet is spliced into HTML responses — never process state, never
proxying. `MARQUEE_DISABLE_BAR=1` at launch is a hard off that the toggle
cannot override. Anything that spawns, signals, or selects a cwd keeps the
full §3.5 rules.

- Code: `internal/proxy/bypass.go`
- Tests: `TestToggleRejectsCrossOriginStateChange`,
  `TestToggleCrossSiteCannotForceBarOn`,
  `TestToggleAllowsSameOriginAndDirectStateChange`,
  `TestToggleNoParamReportsAcrossOrigins`,
  `TestToggleInvalidParamRejectedRegardlessOfOrigin`,
  `TestToggleGuardedByInternalMux`, `TestToggleMethodNotAllowed`,
  `TestToggleInvalidParamRejected`,
  `TestEnvDisablesInjectionAndToggleCannotReenable` in
  `internal/proxy/bypass_test.go`

The other two bypass switches have no endpoint surface: the
`X-Marquee: skip` request header is stripped before the request goes
upstream (the app never sees marquee plumbing —
`TestXMarqueeHeaderNeverForwardedUpstream`), and the
`MARQUEE_DISABLE_BAR` environment variable is read once at startup,
never at request time.

### Bar injection anchors at the true document close

The bar snippet is spliced by byte position only — no HTML parsing, no
evaluation — and the anchor is chosen conservatively so a hostile or
merely unusual upstream cannot make the splice land in the wrong place.
The injector splices immediately before the document's structural
`</body>`: the last `</body>` that lies in ordinary markup. A `</body>`
that appears only inside a `<script>` element (e.g. a trailing analytics
script whose string literal contains `</body>`) or inside an HTML
comment (e.g. a trailing deploy marker) is not the document end, so it
is skipped rather than spliced into — which would otherwise corrupt the
page or silently drop the bar. The scan is a single forward pass, not a
full HTML tokenizer: when a script's own content opens another `<script>`
(HTML's double-escaped state, where the first `</script>` does not close
the element and our region end would diverge from the browser's), the
injector falls open at the last known-good close rather than risk
anchoring inside a still-open script. If no `</body>` qualifies, it
delivers the original bytes untouched, with the upstream `Content-Length`
unchanged. This is not a privilege boundary — the snippet is a fixed
same-origin constant, so a hostile upstream cannot inject attacker script
through it — but it upholds the fail-open law on legitimate-but-unusual
pages.

- Code: `structuralBodyClose` in `internal/proxy/inject.go`
- Tests: `TestStructuralBodyClose` (region logic, including unterminated
  script/comment and the `</body>`-inside-a-string cases) and the
  `trailing script`, `trailing comment`, `textarea`, and
  `only closer inside script` cases in `TestInjectionGoldenFiles`
  (`internal/proxy/inject_test.go`)

### Bar renders only `http(s):` PR links

The injected bar turns the status payload's PR URL into a clickable
anchor. That URL originates from `gh pr view` inside marquee's own
process — `git`/`gh` output, isolated from web and upstream input — and
`gh` yields a canonical `https://github.com/...`, so it is not
attacker-controllable today. The bar validates it anyway, as
defense-in-depth: `safeHttpUrl` parses the value with the URL API and
returns it only when the protocol is `http:` or `https:`. A
`javascript:` or `data:` URL (which would otherwise execute in the
app's top-level origin when clicked) fails the check, so the PR chip is
hidden rather than rendered as a dead or dangerous link. The chip text
(`#<number> <title>`) is written through `textContent`, never
`innerHTML`.

- Code: `safeHttpUrl` in `internal/bar/bar.js`
- Tests: `TestBarScriptEmbedded` in `internal/bar/embed_test.go` pins
  the guard so it cannot be silently removed

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

- Per-process token guard for process-state-changing endpoints (none
  exists yet; the switch endpoint lands in v2). The bar toggle above
  enforces the same-origin half via `Sec-Fetch-Site` but deliberately
  opts out of the token — see its section for why.
