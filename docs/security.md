# Security

marquee sits in an unusually trusted position for a dev tool: it terminates
all your browser's traffic to the wrapped app, injects a script into every
HTML page, and (in v2) will expose an HTTP endpoint that kills and spawns
processes. Its guards are therefore structural, not optional.

This document is the threat-model write-up for spec §6. It enumerates each
threat we defend against, the mitigation **as actually implemented** (with
the file it lives in), and a pointer to the test that proves it. Where a
threat is only partially addressed or deferred to v2, it says so plainly.
An "Accepted residual risks" section at the end records the weaknesses an
adversarial review found and the owner has accepted for a local dev tool.

Every "proven by" pointer names a real, passing test. A mitigation with no
test is labelled **unverified** rather than claimed.

## Threat 1 — LAN peers

**Threat.** Anyone on your network reaching the proxy — and through it your
dev app (which has no auth of its own) and, in v2, the switch endpoint.

**Mitigation (implemented).** Loopback-only binding is the hard default.
`validateListen` refuses to start when `--listen` is not a loopback address;
`loopbackHost` treats `localhost`, `*.localhost`, and any IP for which
`net.IP.IsLoopback` is true (the whole `127.0.0.0/8` range, plus `::1`) as
loopback. A non-loopback `--listen` is a hard refusal (exit 1, error naming
the flag) unless `--unsafe-listen` is also passed; when it is, marquee starts
but prints a persistent, unmissable banner. `printUnsafeListenWarning` writes
that banner **straight to stderr, never through the logger**, so `--quiet`
cannot suppress it.

Attach mode (`marquee attach --upstream <url>`) is a pure proxy in front of a
server the user runs themselves. Its `--listen` obeys the same rule, and its
`--upstream` gets the symmetric guard: `validateUpstream` refuses a
non-loopback upstream (exit 1) unless `--unsafe-listen` is passed, and it
inspects only the host string — a non-loopback upstream is rejected **without
ever dialing it**, so a refusal performs no network action. A missing, empty,
unparseable, or non-`http(s)` `--upstream` is a usage error (exit 2) before
any network action (`parseUpstream`).

- Code: `loopbackHost`, `freePort` in `cmd/marquee/main.go`; `validateListen`,
  `validateUpstream`, `parseUpstream`, `printUnsafeListenWarning`,
  `printUnsafeUpstreamWarning` in `cmd/marquee/options.go`; the attach flows
  in `cmd/marquee/attach.go`.
- Proven by: `TestLoopbackHost` (`cmd/marquee/main_test.go`);
  `TestValidateListenLoopback`,
  `TestValidateListenNonLoopbackRefusedWithoutFlag`,
  `TestValidateListenNonLoopbackAllowedWithFlag`,
  `TestValidateListenInvalidAddress`, `TestUnsafeListenWarningIsLoud`,
  `TestUnsafeBannerNotSuppressibleByQuiet` (the `--quiet` cannot silence the
  banner assertion) in `cmd/marquee/options_test.go`;
  `TestValidateUpstreamLoopback`, `TestValidateUpstreamNonLoopbackRefused`
  (the abuse test: non-loopback — including userinfo `127.0.0.1@evil.com`,
  the decimal form `2130706433`, and a `localhost.evil.com` suffix — refused
  with no network action), `TestValidateUpstreamNonLoopbackAllowedWithFlag`,
  `TestUnsafeUpstreamWarningIsLoud`, `TestParseAttachArgsUpstreamRequired`,
  `TestParseAttachArgsBadScheme` in `cmd/marquee/attach_test.go`.

**Caveat.** For the `localhost` / `*.localhost` names, `loopbackHost` decides
loopback by *name*, not by resolving and checking the IP. See Accepted
residual risk 3.

## Threat 2 — DNS rebinding

**Threat.** A malicious website that resolves its own domain to `127.0.0.1`
so the victim's browser sends requests to the proxy with an
attacker-controlled page context — reaching marquee's own endpoints.

**Mitigation (implemented).** A Host allowlist on every `/__marquee/*`
endpoint. `InternalMux.ServeHTTP` validates the request `Host` (port
stripped, case-insensitive) before any handler runs; anything not on the
allowlist gets a 403. The built-in allowlist is `localhost`, `127.0.0.1`,
`::1`, and `*.localhost` (RFC 6761 reserves `.localhost` for loopback, so no
third party can register it — safe to trust by default). `*.lvh.me` is **not**
trusted by default (it is a third-party public wildcard that resolves every
subdomain to `127.0.0.1`; trusting it would outsource the rebinding boundary
to an external operator), so operators who rely on it must opt in with
`--allow-host '*.lvh.me'`. Wildcards are anchored on a dot boundary, so
`*.lvh.me` matches `app.lvh.me` but neither the bare apex `lvh.me` nor
lookalikes like `evil-lvh.me` or `lvh.me.evil.com`. There is no way to
register an internal endpoint outside the guard: the underlying
`http.ServeMux` is unexported and only reachable via `Handle`/`HandleFunc`.

The `/__marquee/` namespace is never proxied to the upstream, so these paths
cannot leak to the app either.

Note the deliberate asymmetry: proxied **app** traffic keeps its `Host`
untouched (multi-tenant subdomain routing needs it); only marquee's own
endpoints validate `Host`.

- Code: `InternalMux`, `hostAllowed`, `hasLabelSuffix`, `hostSuffix` in
  `internal/proxy/internal.go`; the `Host`-preserving `Rewrite` in
  `internal/proxy/proxy.go`; `--allow-host` parsed in
  `cmd/marquee/options.go`, plumbed via `proxy.Config.AllowHosts` in
  `cmd/marquee/{main.go,attach.go}`.
- Proven by: `TestInternalHostGuard` (rejects `evil.com`, the suffix
  lookalike `localhost.evil.com`, the prefix lookalike `evil-localhost.com`,
  and `lvh.me.evil.com`; allows the built-ins; and asserts `no-store` on
  every response; the trailing `hostAllowed("")` assertion proves an **empty
  Host** is rejected), `TestInternalMuxRegistrationGoesThroughGuard`,
  `TestInternalNamespaceNeverProxied`, `TestHostPreservedVerbatim` (the
  app-traffic asymmetry) in `internal/proxy/proxy_test.go`;
  `TestHostGuardEnforcedThroughMux` (403 on `/__marquee/status` and
  `/__marquee/bar.js` with `Host: evil.com`) in
  `internal/status/status_test.go`; `TestAllowHostFlagReachesGuard`,
  `TestParseArgsAllowHostRepeatable` (the flag reaches the guard end to end)
  in `cmd/marquee/`.

## Threat 3 — Cross-site requests to state-changing endpoints (CSRF)

**Threat.** Any page in the browser issuing a request to a marquee endpoint
that changes state (e.g. a background tab firing `GET
/__marquee/toggle?bar=off` via `<img src>` on an interval to persistently
suppress the bar's safety indicator; in v2, a page hitting
`/__marquee/switch`).

**Mitigation (implemented, partial).** The only state-changing endpoint in
v1 is the bar toggle. The Host allowlist is a DNS-rebinding defense, not a
CSRF one, so the toggle's mutating path (`bar=on|off`) additionally consults
`Sec-Fetch-Site`, which browsers set and page JS cannot forge: a typed
address-bar navigation sends `none` and a same-origin fetch sends
`same-origin` (both **allowed**), while a cross-site or same-site
cross-origin page sends `cross-site`/`same-site` (**rejected with 403**,
state unchanged). The header is absent for `curl`/scripted use, which stays
allowed — its absence is a hardening signal, not a gate, so the documented
"type it in the address bar" and CLI uses never break. The check applies only
to the mutating path: a no-parameter state report changes nothing and stays
open even cross-site; an invalid value is a 400 before any origin check. The
toggle is GET-only (405 otherwise) and, like every internal endpoint, sets
`Cache-Control: no-store`.

- Code: `sameOriginOrDirect`, `handleToggle` in `internal/proxy/bypass.go`.
- Proven by: `TestToggleRejectsCrossOriginStateChange` (cross-site/same-site
  `bar=off` → 403 **and** state unchanged), `TestToggleCrossSiteCannotForceBarOn`,
  `TestToggleAllowsSameOriginAndDirectStateChange` (same-origin / typed-nav /
  curl all allowed), `TestToggleNoParamReportsAcrossOrigins`,
  `TestToggleInvalidParamRejectedRegardlessOfOrigin`,
  `TestToggleGuardedByInternalMux`, `TestToggleMethodNotAllowed`,
  `TestToggleInvalidParamRejected`,
  `TestEnvDisablesInjectionAndToggleCannotReenable` in
  `internal/proxy/bypass_test.go`.

**Deferred to v2.** The §3.5 rules for *process*-state-changing endpoints —
a per-process random token minted at startup, embedded only via the injected
snippet, and required as a header on every state-changing call — are **not
implemented**; no such endpoint exists yet. The `/__marquee/switch` endpoint
lands in v2 and must enforce them. The toggle deliberately opts out of the
token because it flips only whether the bar snippet is spliced into HTML — it
never touches process state or proxying — so `Sec-Fetch-Site` alone is the
proportionate guard. `MARQUEE_DISABLE_BAR=1` at launch is a hard off the
toggle cannot override.

## Threat 4 — Command / path injection via slug

**Threat.** A `switch` slug flowing toward process spawning and cwd
selection, letting HTTP input shell out or escape the intended directory.

**Status: deferred (no attack surface in v1).** There is no `switch`
endpoint and no slug handling in the code today, so there is nothing to
inject into over HTTP. The only process spawn in v1 is the child dev server,
launched by `runner.New` from the operator's own `--` argv (`opts.command`
from the CLI), never from request data. The `#nosec G204` annotations on the
subprocess launches in `runner`, `gitinfo`, `ghinfo`, and the browser opener
document exactly this: the argv is a fixed literal or operator-supplied, never
HTTP-derived. The pidfile path is derived from a sha256 of the listen address
under the user cache dir (`#nosec G304`), never from request input.

When the v2 switch endpoint lands, the slug must be only an exact-match lookup
key into the parsed `git worktree list` set — never path-joined, never
shelled — and any `--switch-hook` must be operator-configured on the command
line, never influenced by HTTP input.

- Code: `runner.New` call site in `cmd/marquee/main.go`; `internal/runner/runner.go`;
  `pidfilePath` / `warnStaleChild` in `cmd/marquee/pidfile.go`.
- Proven by: the runner's spawn/signal tests (`TestStartSetsOwnProcessGroup`,
  `TestEnvReachesChild`, `TestStopKillsGrandchildren`, …) in
  `internal/runner/runner_test.go` exercise the fixed-argv spawn path. The
  slug-injection defense itself is **unverified** — it cannot be tested until
  the endpoint exists. AGENTS.md requires an abuse test in this suite for
  anything that spawns, signals, or selects a cwd, which the v2 endpoint will
  carry.

The one signal-adjacent path that *does* exist — the stale-child pidfile
warning — is hardened against corrupt input: see Threat 7's pidfile note.

## Threat 5 — Malicious / compromised upstream responses

**Threat.** A hostile or compromised upstream returning bytes the injector
must handle: crafted HTML meant to make the splice land in the wrong place, a
compressed or truncated body, or an oversized body meant to exhaust memory.

**Mitigation (implemented).** Injection is **byte-splicing only** — no HTML
parsing, no evaluation. The snippet is a fixed same-origin constant, so a
hostile upstream cannot inject attacker script through the mechanism; the
guarantee it upholds is fail-open correctness on legitimate-but-unusual pages.

- **Structural anchor.** `structuralBodyClose` splices immediately before the
  document's *structural* `</body>` — the last one in ordinary markup. A
  `</body>` that appears only inside a `<script>` string literal or an HTML
  comment is skipped. On HTML's double-escaped script state (a nested
  `<script` inside script content, where the first `</script>` does not close
  the element and our region end would diverge from the browser's), the
  scanner fails open at the last known-good close rather than risk anchoring
  inside a still-open script. If nothing qualifies, the original bytes are
  delivered untouched with `Content-Length` unchanged.
- **Header-only candidate check + encoding gate.** `isInjectionCandidate`
  decides from headers alone (2xx, `text/html`, not an event-stream, not an
  internal path, not an iframe destination) before any body byte is read, so
  non-candidates keep streaming. `isIdentityEncoding` passes through anything
  not plainly uncompressed, because splicing into compressed bytes would
  corrupt the page.
- **Size cap + fail-open.** `readCapped` buffers at most `injectSizeCap`
  (~10 MB); an over-cap body passes through untouched (unbuffered when
  Content-Length announces the size). Every failure mode — read error, panic
  (except `http.ErrAbortHandler`, which keeps propagating), no qualifying
  anchor — restores the original bytes. `modifyResponse` always returns nil,
  so a working upstream response is never replaced by a proxy error.
- **The bar script is inert.** `internal/bar/bar.js` never reads page
  content, only fetches same-origin `/__marquee/status`, contains no `eval`
  and no `import(`, and makes no external requests. It renders the PR link
  through `safeHttpUrl`, which returns the URL only for `http:`/`https:`
  (a `javascript:`/`data:` URL is dropped), and writes chip text via
  `textContent`, never `innerHTML`.

- Code: `injector`, `modifyResponse`, `isInjectionCandidate`,
  `isIdentityEncoding`, `structuralBodyClose`, `readCapped` in
  `internal/proxy/inject.go`; the `Accept-Encoding: identity` rewrite in
  `internal/proxy/proxy.go`; `safeHttpUrl` in `internal/bar/bar.js`.
- Proven by: `TestStructuralBodyClose` (region logic, including both
  double-escaped-script cases, unterminated script/comment, and
  `</body>`-inside-a-string), `TestInjectionGoldenFiles` (the `trailing
  script`, `trailing comment`, `textarea`, `only closer inside script`, `no
  closing body`, and `iframe`/`json`/`500` skip cases, each asserting
  `Content-Length` equals the bytes received),
  `TestInjectionRecomputesContentLengthForChunkedUpstream`,
  `TestHugeBodyWithContentLengthPassesThrough`, `TestHugeChunkedBodyPassesThrough`,
  `TestTruncatedUpstreamBodyFailsOpen`, `TestCompressedResponsePassesThrough`,
  `TestContentEncodingGate`, `TestEventStreamSkippedAndNotBuffered`,
  `TestCapOverrunSeamClosesUpstreamBodyOnce`,
  `TestErrAbortHandlerPanicNotSwallowed`, `TestInternalPathNeverCandidate` in
  `internal/proxy/inject_test.go`; `TestBarScriptEmbedded` (pins the
  `safeHttpUrl` guard and forbids `eval(`, `import(`, and any absolute
  `http(s)://` URL in the script) in `internal/bar/embed_test.go`.

See Accepted residual risk 2 for the memory-amplification limitation this
buffering introduces.

## Threat 6 — Supply chain (ours)

**Threat.** The binary people install must be what CI built from reviewed
source, so a compromised dependency or a swapped Action cannot slip code into
the release.

**Mitigation (implemented).** The toolchain is kept small and continuously
scanned.

- **Zero runtime dependencies.** `go.mod` requires nothing beyond the
  standard library, keeping the dependency attack surface at zero.
- **`govulncheck` in CI.** Every push and pull request runs `govulncheck
  ./...` (pinned) against the standard library and any future dependency,
  failing the build on a known, reachable vulnerability.
- **`gosec` in CI** via `golangci-lint` (alongside `errorlint`). Findings are
  triaged, not blanket-ignored: real issues are fixed, and the few false
  positives carry a narrowly-scoped `#nosec` justified by why the input
  cannot come from HTTP (the subprocess launches in `runner`, `gitinfo`,
  `ghinfo`, and the startup port diagnostics run fixed or operator-supplied
  argv, never request data).
- **Actions pinned by commit SHA.** Every `uses:` in `.github/workflows/` is
  pinned to a full commit SHA with a version comment, so a moved tag cannot
  swap the action out from under us.
- **Dependabot** (`.github/dependabot.yml`) watches the `github-actions` and
  `gomod` ecosystems weekly and opens PRs that go through the same CI and
  human review.
- **AI-written code gets the same human review as any contributor**, plus the
  MSEC-T2 adversarial agent pass — the implementer reviewing itself is
  worthless.

- Config: `.github/workflows/ci.yml`, `.golangci.yml`, `.github/dependabot.yml`.
- Proven by: CI configuration, not unit tests — this threat is enforced by the
  pipeline gates above, which are **not** exercised by `go test`.

## Threat 7 — Information disclosure

**Threat.** The status endpoint exposes repository metadata — branch names,
worktree paths, repo root, PR number/title/URL, child state.

**Mitigation (implemented).** This is acceptable on loopback with the Host
guard (Threat 2) in place, and the payload is deliberately bounded: repository
metadata only. No environment-variable **values**, no tokens, no operator
secrets, and no command lines appear in any endpoint response or log line.
`GET /__marquee/status` is GET-only, sets `Cache-Control: no-store`, and is
Host-guarded by construction (registered through the guarded mux). The one
place an environment variable is named is the toggle's state report, which
discloses the *name* `MARQUEE_DISABLE_BAR` when the launch-time hard-off is
active — never its value or any other variable.

The stale-child pidfile warning is the one path that reads a
previous-run artifact and acts on it. It is hardened against corrupt input:
`warnStaleChild` removes a pidfile whose contents do not parse, or whose
recorded pgid is `<= 1` (so it can never suggest the catastrophic `kill -TERM
-1`), and for a still-alive group it only *warns* — it **never signals the
group itself**. `groupAlive` uses signal 0 (an existence probe, not a real
signal).

- Code: `statusHandler`, `Register` in `internal/status/status.go`;
  `warnStaleChild`, `groupAlive` in `cmd/marquee/pidfile.go`.
- Proven by: `TestStatusJSONShape`, `TestStatusReportsPosition`,
  `TestStatusEmptySnapshotSerializesEmptyWorktreeList`,
  `TestStatusMethodNotAllowed`, `TestHostGuardEnforcedThroughMux` in
  `internal/status/status_test.go`;
  `TestWarnStaleChildGarbageCleansUpSilently` (a **corrupt pidfile** is
  removed without signalling any group),
  `TestWarnStaleChildPgidOneCleansUpSilently` (pgid 1 never warns),
  `TestWarnStaleChildDeadGroupCleansUpSilently`,
  `TestWarnStaleChildAliveGroupWarnsWithoutKilling` (an alive group is warned
  about but **never killed**) in `cmd/marquee/pidfile_test.go`.

## Standing rules

Enforced via AGENTS.md, applied to every PR:

- New internal endpoints inherit the Host allowlist and `no-store` guards by
  construction — they register through `InternalMux`, not an ad-hoc mux.
- Anything that spawns, signals, or selects a cwd requires an abuse test in
  this suite (`internal/proxy`, `cmd/marquee`).
- Any security-relevant behavior change updates this file in the same PR.

## Accepted residual risks

An adversarial review found the following. The owner accepts them for a
local, single-user dev tool; they are documented here, not fixed.

1. **`freePort` close-then-hand TOCTOU** (`freePort` in
   `cmd/marquee/main.go`). In wrapper mode marquee binds `127.0.0.1:0`, reads
   the assigned port, **closes the listener**, and hands the bare port number
   to the child via `PORT`. Between the close and the child's own bind, a
   same-user local process could win the race and bind the internal port,
   MITMing the dev app behind the proxy. Scope: same-user, loopback only — an
   attacker already running code as you. Accepted; a future fix is
   file-descriptor inheritance (hand the child the socket, never a bare port).

2. **Injectable HTML is fully buffered, with bounded memory amplification.**
   The injector reads a candidate `text/html` body whole (up to
   `injectSizeCap`, ~10 MB) before forwarding (`readCapped` in
   `internal/proxy/inject.go`), so genuinely streamed/SSR HTML loses its
   streaming behind marquee, and a hostile upstream can pin up to ~10 MB per
   concurrent injectable request. Scope: local. This is a documented
   limitation — spec §8 already lists streamed HTML as v1-unsupported — and
   the cap bounds per-request memory; non-HTML, event-streams, and over-cap
   bodies still stream through untouched.

3. **Loopback is decided by *name* for `localhost` / `*.localhost`**
   (`loopbackHost` in `cmd/marquee/main.go`). These names are trusted by
   string match, **not** by resolving them and confirming the result is a
   loopback IP. Because `.localhost` is RFC 6761 loopback-reserved, a
   conforming resolver always maps it to loopback, so this is safe in
   practice — but the guard does not *verify* it. Only literal IP addresses
   are checked with `net.IP.IsLoopback`. The threat model does not claim that
   loopback is always IP-verified; for the two reserved names it is a
   name-based decision.

## Not yet implemented (v2)

- The per-process token guard and the `/__marquee/switch`
  process-state-changing endpoint (Threat 3 / Threat 4). None exists yet; the
  switch endpoint and its §3.5 guards land in v2 and must ship with an abuse
  test in this suite.
