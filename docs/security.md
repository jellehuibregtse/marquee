# Security

marquee sits in an unusually trusted position for a dev tool: it terminates
all your browser's traffic to the wrapped app, injects a script into every
HTML page, and exposes an HTTP endpoint (`POST /__marquee/switch`) that kills
and spawns processes. Its guards are therefore structural, not optional.

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
dev app (which has no auth of its own) and the switch endpoint.

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
suppress the bar's safety indicator; or a page hitting `/__marquee/switch` to
make the proxy kill and respawn processes).

**Mitigation (implemented).** There are two state-changing endpoints: the
cosmetic bar toggle and the process-spawning worktree switch, each guarded in
proportion to its power. The Host allowlist is a DNS-rebinding defense, not a
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

**Process-state-changing endpoint (implemented).** The §3.5 rules for the
*process*-state-changing endpoint are now implemented for
`POST /__marquee/switch` (the worktree switcher), which kills and respawns the
child dev server. It carries a **stricter** guard stack than the cosmetic
toggle, all of which must pass before any process action (see the "Worktree
switch endpoint" section below for the full stack and the token lifecycle):

1. **POST only** — the guarded mux answers any other method with 405.
2. **Strict same-origin** — `Sec-Fetch-Site: same-origin`, or (when the header
   is absent) an `Origin` whose scheme+host+port equals the request Host.
   Unlike the toggle, `Sec-Fetch-Site: none` (a typed address-bar navigation)
   is **not** accepted: a process-spawning endpoint must be provably
   same-origin. Otherwise 403.
3. **Per-process token** — a 256-bit `crypto/rand` token minted once at
   startup, delivered to the page **only** through the injected
   `<marquee-bar token="…">` attribute, echoed by bar.js as the
   `X-Marquee-Token` header, and compared with
   `crypto/subtle.ConstantTimeCompare`. Missing/wrong token → 403.

The toggle deliberately opts out of the token because it flips only whether the
bar snippet is spliced into HTML — it never touches process state or proxying —
so `Sec-Fetch-Site` alone is the proportionate guard. `MARQUEE_DISABLE_BAR=1`
at launch is a hard off the toggle cannot override.

The token defends a **browser-driven** cross-site request on the loopback
default; it is not a secret against an active network reader. Under
`--unsafe-listen`, proxied app traffic is no longer Host-guarded, so a LAN peer
can fetch an injected page, read the `token` attribute, and replay it from a
non-browser client forging `Host` and `Sec-Fetch-Site`. That falls inside the
Threat 1 accepted risk — loopback is the hard default and `--unsafe-listen`
prints a persistent, non-suppressible warning — not a defense the token was
meant to provide.

- Code: `sameOrigin`, `originMatchesHost`, `Handler.tokenOK`, `Handler.serve`
  in `internal/switcher/switcher.go`; the token minted by `mintToken` in
  `cmd/marquee/main.go`, threaded through `proxy.Config.SwitchToken` into the
  injected snippet (`barSnippetForToken` in `internal/proxy/inject.go`) and
  into `switcher.Config.Token`.
- Proven by: `TestCrossOriginRejectedNoProcessAction` (cross-site, same-site,
  `none`, missing-both, and Origin-mismatch all → 403 **and** no restart),
  `TestOriginFallbackMatchesHostIsAllowed`,
  `TestMissingOrWrongTokenRejectedNoProcessAction`,
  `TestEmptyTokenRejectsEveryRequest`, `TestHostGuardEnforced`,
  `TestMethodNotAllowed` in `internal/switcher/switcher_test.go`;
  `TestInjectCarriesSwitchToken`, `TestInjectWithoutTokenUsesTokenlessSnippet`,
  `TestBarSnippetForToken` (the token reaches the page only through the
  injected element) in `internal/proxy/switch_test.go`.

## Threat 4 — Command / path injection via slug

**Threat.** A `switch` slug flowing toward process spawning and cwd
selection, letting HTTP input shell out or escape the intended directory.

**Mitigation (implemented).** The `POST /__marquee/switch` slug flows toward a
`cwd` selection (the runner restarts the child in a worktree directory), so it
is the one place HTTP input reaches process control. It is defended by never
letting the request choose a path:

- **The slug is only ever an exact-match lookup key.** `resolveWorktree`
  compares the posted slug against the `Slug` of each worktree parsed from
  git's own `git worktree list --porcelain` output (via `gitinfo.Collect`,
  reusing the poller's parser). The **absolute path handed to the runner comes
  from git's output**, never from the request. An unknown slug, or any
  traversal shape (`../evil`, `/absolute/path`, `..%2f..`, `feature/..`, `.`,
  empty), simply matches no worktree slug and is rejected with **400 and no
  process action**. An ambiguous duplicate slug (more than one match) is
  likewise rejected — the switch never acts on an unresolved target.
- **No path-join, no `filepath.Clean`-and-use, no stat of a request-derived
  path, no shell.** The slug is never concatenated with a base directory,
  never cleaned into a path, never passed to a shell. The runner's spawn is
  still the operator's original `--` argv; only its `cwd` changes, and only to
  a git-reported worktree path.
- **The existing `#nosec G204` annotations still hold.** The argv in `runner`,
  `gitinfo`, `ghinfo`, and the browser opener is a fixed literal or
  operator-supplied, never HTTP-derived; the switch changes the runner's `cwd`
  to a git-reported path, not the argv.

**`--switch-hook` does not widen this surface.** The optional `--switch-hook`
command (per-worktree bootstrap such as `bundle install`) is **operator input
from the CLI flag**, exactly like the wrapped dev command itself — it is never
derived from the HTTP request or the slug. The only request-derived value in a
switch remains the validated slug, which selects *which* git-reported worktree
to act on but never contributes a single byte to the hook command. The switcher
runs the hook with `exec.CommandContext(ctx, "sh", "-c", hookCmd)` and its `cwd`
set to git's own worktree path (never a request-derived path), so it introduces
no new way for HTTP input to choose a command or a directory. The `sh -c` form
is deliberate — operators write pipelines and `&&` chains — and carries a
justified `#nosec G204` recording that `hookCmd` is operator-only. Because the
hook runs before the current child is stopped, a failing hook (non-zero exit or
timeout) fails the switch without touching the running child — no restart, no
revert. When a switch does get far enough to stop the child before failing, the
revert re-runs the hook in the previous worktree — still the operator's own CLI
command, cwd set to git's own worktree path — so a cleanup step can clear stale
process-manager state before the child restarts there; this reuses the same
operator-only command and adds no request-derived input. See the "Worktree
switch endpoint" section for where it sits in the guard/switch sequence.

The one signal-adjacent path from v1 — the stale-child pidfile warning — is
hardened against corrupt input: see Threat 7's pidfile note.

- Code: `resolveWorktree`, `Handler.serve` in `internal/switcher/switcher.go`;
  `gitinfo.Collect` / `parseWorktrees` in `internal/gitinfo/gitinfo.go`; the
  runner `cwd` change in `Runner.Restart` (`internal/runner/runner.go`).
- Proven by: `TestUnknownOrTraversalSlugRejectedNoProcessAction` (every unknown
  and traversal shape → 400 with **no restart and no repoint**),
  `TestValidSwitchAgainstRealRepoRestartsAndRepoints` (a real temp repo with
  two worktrees: a valid, same-origin, correctly-tokened switch restarts the
  child in **git's** worktree path and repoints the pollers there),
  `TestRestartFailureRevertsAndReportsFailure` (a failed restart reverts, never
  repoints to the target, and reports failure) in
  `internal/switcher/switcher_test.go`.

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

## Injected-page CSP relaxation

**What it is.** When — and only when — marquee successfully splices the bar
into a response, it rewrites that response's enforcing `Content-Security-Policy`
just enough for the bar's own same-origin resources to load. This is a
deliberate, minimal relaxation of the proxied app's *own* CSP, required because
the bar is a real external module script (`/__marquee/bar.js`) plus a
same-origin fetch (`/__marquee/status`): a page CSP whose `script-src` omits
`'self'` blocks the script, so the custom element never defines and the bar
never appears, even though everything else (proxy, injection, endpoints)
works. It is on by default and opt-out via `--keep-csp`.

**Exactly what it changes.** `relaxCSPForBar` ensures `'self'` in two
directives and nothing else:

- **Scripts.** Whichever directive governs `<script>` *elements* gains
  `'self'`: `script-src-elem` if present (it overrides `script-src` for
  elements), else `script-src`, else — if only `default-src` exists — a **new**
  explicit `script-src` is added as a copy of `default-src`'s sources plus
  `'self'` (so `default-src` itself is left intact and no other resource type
  is broadened). If no script-governing directive exists, nothing is added.
- **Connect.** `connect-src` gains `'self'`; else, if only `default-src`
  exists, a new explicit `connect-src` is derived from it the same way.
- **`'self'` is added idempotently** — a directive that already allows `'self'`
  is untouched. The one rewrite of an existing source is the special case where
  a governing directive is exactly `'none'`: since `'none'` cannot legally be
  mixed with sources, it is replaced by `'self'` (the minimal way to permit our
  resource). No other existing source is ever removed.

**What it does NOT change.** Report-only CSP
(`Content-Security-Policy-Report-Only`) is left completely untouched — it never
blocks. No directive other than the script- and connect-governing ones is
modified; `img-src`, `style-src`, `default-src`, etc. are preserved verbatim,
as is directive order. Only same-origin `'self'` is ever granted — never a
third-party host. A response marquee did **not** inject into (non-HTML, non-2xx,
event-stream, over-cap, iframe, a bypass-switched request, or any fail-open
pass-through) keeps its CSP byte-for-byte: the rewrite is coupled to an actual
successful splice, at the same point the spliced body is committed.

**Tradeoff and scope.** marquee is weakening a security header the app set,
which is only acceptable because marquee is a loopback-only dev tool that
already terminates all the app's traffic and injects script into every page —
the CSP relaxation grants strictly less than the injection itself already
assumes. The change is minimal (two directives, `'self'` only), fail-open (a
malformed or surprising CSP is left verbatim; a panic in the rewrite cannot
escape — `relaxCSPForBar` recovers and the caller in `inject` is panic-guarded
too), and reversible per run with `--keep-csp`, which leaves every response's
CSP exactly as the app sent it (the bar may then not load if the app's CSP
forbids same-origin scripts).

- Code: `relaxCSPForBar`, `relaxCSPValue`, `ensureScriptSelf`,
  `ensureConnectSelf`, `ensureSelf` in `internal/proxy/csp.go`; the gated
  call on the successful-splice path and the `relaxCSP` flag in
  `internal/proxy/inject.go`; `Config.RelaxCSP` in `internal/proxy/proxy.go`;
  the `--keep-csp` opt-out in `cmd/marquee/options.go`, plumbed as
  `RelaxCSP: !opts.keepCSP` in `cmd/marquee/{main.go,attach.go}`.
- Proven by: `TestRelaxCSPValue` (the full rewrite table: script-src /
  script-src-elem / default-src fallbacks, connect-src, idempotency, the
  `'none'` → `'self'` special case, no-op when no governing directive,
  case-insensitive names, deterministic reserialization),
  `TestRelaxCSPForBarNoHeaderNoChange`,
  `TestRelaxCSPForBarLeavesReportOnlyUntouched`,
  `TestRelaxCSPForBarRewritesAllEnforcingHeaders`,
  `TestRelaxCSPForBarGarbageValueLeftUnchanged`,
  `TestInjectRelaxesCSPWhenBarInjected`,
  `TestInjectLeavesCSPUnchangedWhenRelaxDisabled`,
  `TestInjectLeavesCSPUntouchedOnNonInjectedResponse` in
  `internal/proxy/csp_test.go`.

## Worktree switch endpoint

`POST /__marquee/switch` is the highest-risk surface in marquee: it kills and
respawns the wrapped dev server in a different git worktree. It is registered
through the guarded `InternalMux` (so it inherits the Host allowlist and
`no-store` by construction) and enforces a full guard stack — **all** guards
must pass, in this order, before any process action; every failure returns a
machine-readable JSON error and takes no action:

1. **Method** — POST only; the mux method pattern answers others with 405.
2. **Host allowlist** — the guarded mux rejects a forbidden Host with 403
   (Threat 2).
3. **Strict same-origin** — `Sec-Fetch-Site: same-origin`, or an `Origin`
   matching the request Host when the header is absent; `none` is rejected.
   Otherwise 403 (Threat 3).
4. **Constant-time token** — `X-Marquee-Token` must equal the per-process
   token under `subtle.ConstantTimeCompare`; otherwise 403 (Threat 3).
5. **Concurrency lock** — a mutex serializes switches; a second switch while
   one is in progress gets 409 `busy`, so two POSTs cannot race the runner.
6. **Strict slug validation** — exact match into `git worktree list`; unknown
   or traversal shapes get 400 `unknown_slug` (Threat 4).
7. **Dirty safety** — if the current worktree has uncommitted changes and the
   target is not the main worktree and the request did not set `confirm=true`,
   the switch is refused with 409 `dirty` (a machine-readable reason the bar
   uses to prompt for confirmation). Switching **back to the main worktree is
   always allowed**, dirty or not.

Request-body parsing precedes the concurrency lock: a malformed or empty body
returns 400 `bad_request` before step 5. This is side-effect-free (no git
subcommand, no process action), so it does not change the "no action before the
guards pass" guarantee.

Only after all guards pass does marquee — optionally — run the operator's
`--switch-hook` in the **target worktree** (`cwd` = git's own worktree path), then
stop the child and `runner.Restart(ctx, worktreePath)` (which reclaims marquee's
internal port before the spawn — see "Reclaiming the internal port" below),
TCP-health-poll the new child, **require that the child is still running** (a
passing probe alone is not proof it booted — see the shutdown-path note), and —
only once it is both healthy and alive — **repoint both the gitinfo and ghinfo
pollers** to the new worktree (otherwise the bar keeps reporting the old worktree — the exact lie the
tool exists to prevent). `Poller.Repoint` swaps the collection directory under
the same mutex that guards the cached snapshot, so a concurrent status read
never sees a torn state (covered by `-race` tests). While a switch is in
progress, proxied HTML navigations receive a self-refreshing "switching to
*slug*…" page (the slug is HTML-escaped defensively) and non-HTML requests a
plain 503; the switch POST itself and the status endpoint are on the internal
namespace and are never blocked by that page.

**A failed switch is non-fatal and reverts (do-no-harm).** A switch can fail
because the target worktree's dev server never comes up (e.g. its dependencies
are not installed): the restarted child exits, or never accepts connections
within the health timeout. Two properties keep such a failure from stranding the
user:

- **The shutdown path is switch-aware.** marquee wraps a dev server; when the
  child exits *on its own*, marquee shuts down (that is the correct "your dev
  server died" behavior). But an exit that is part of a restart or switch is
  **not** the app dying — it is expected. The runner distinguishes the two: a
  child exit closes `Runner.Terminated()` (the channel `cmd/marquee` selects on
  to shut down) **only** when no managed lifecycle window is open. `Restart`
  opens such a window around its own stop→start, and the switcher opens one
  around the *entire* switch — target restart, health wait, and any revert — via
  `BeginManaged`/`EndManaged`. Closing the window never retroactively fires the
  signal, so an exit that happened inside it stays the switcher's to handle. The
  result: the transient stop of a healthy switch never shuts marquee down, and a
  switched-into dev server that fails to boot never shuts marquee down either.
- **A switch requires a *live* child, not just an answering port.** A TCP health
  probe can pass by connecting to a **stale listener** — an escaped daemon left
  by the old child, still holding the internal port (see "Reclaiming the internal
  port"). So a passing probe alone does not prove the new child booted: the
  switch also checks `ChildAlive` (the runner's `StateRunning`) right after the
  probe. If the child has exited, the switch is a failure and reverts — it never
  reports `{"ok":true}` on a dead child, which is what previously let a doomed
  switch declare success, close its managed window, and then have the child's
  exit observed as *unmanaged*, firing `Terminated` and shutting marquee down.
  Freeing the port (above) also makes the probe honest; the liveness check is the
  belt to the port-reclaim's braces.
- **The switcher reverts, once, and never reports a fake success.** If the
  target does not become healthy, the switcher restarts the child back in the
  **previous** worktree, repoints the pollers back, and restores its
  `currentDir`. If the reverted child is healthy, marquee continues on the
  previous worktree as if the switch never happened. The response is always a
  non-2xx `switch_failed` with a `reverted` boolean — never `{"ok":true}`. If
  the target *and* the revert both fail to boot, marquee **still** does not
  hard-exit: it stays alive with the child down, the proxy serves its existing
  "starting/unavailable" page (`ErrorHandler`/`serveStarting`), and the response
  reports `switch_failed` with `reverted:false` so the user can retry or switch
  back. A dead-but-alive marquee the user can recover from beats one that
  vanished mid-switch. The old child is fully stopped (whole process group) by
  the restart before the revert spawns, so a failed switch never orphans a
  process.

**Switch hook (`--switch-hook`).** A fresh worktree often cannot boot until its
dependencies are installed (git-sourced gems, `node_modules`), so a switch into
it would otherwise fail. The optional `--switch-hook` command bootstraps the
target worktree first: it runs in the target directory (`cwd` = git's worktree
path) **before** the child is restarted there, via
`exec.CommandContext(ctx, "sh", "-c", hookCmd)`, bounded by a hook timeout
(default 5 minutes; bootstrapping is slow) that runs inside the same managed
window and busy lock as the rest of the switch. Its stdout and stderr are
streamed to marquee's stderr, prefixed `switch-hook: …`, so the operator sees
progress and errors. It is **off by default** and, as Threat 4 records, is
operator-only CLI input — never influenced by the HTTP request or the slug.
Because the hook runs **before the current child is stopped**, a hook failure
(non-zero exit or timeout) fails the switch **without touching the running
child**: no restart into the target, and no revert either — bouncing a server
that never moved would be pointless work that can itself race a restart into a
stale process-manager socket and leave the dev server dead after a harmless hook
error. The response is `switch_failed` (never `{"ok":true}`), the dev server
left running exactly where it was. When a switch instead fails **after** the
child has been stopped (a failed boot, health timeout, or dead child), the
revert **re-runs the hook** in the previous worktree before restarting the child
there. The forward stop can leave the previous worktree's process manager with a
stale socket (e.g. `.overmind.sock`) that blocks a clean reboot ("already
running"); an operator hook such as `rm -f .overmind.sock` clears it so the
revert actually recovers. Re-bootstrapping a worktree that already worked is
mildly wasteful, but that cost is minor next to a dead dev server, and a hook
failure on the revert is only logged (not fatal) so recovery still attempts the
restart. The hook is operator-only CLI input either way, so running it on the
revert widens no attack surface.

**Reclaiming the internal port on a switch (kill-by-port).** marquee stops the
child by killing its whole process group. A manager such as `overmind` or a
`tmux`-based runner daemonizes its server into a *separate* session, so that
server is not in the killed group: it survives the stop and keeps holding
marquee's internal port. Left alone this both blocks the new child from binding
*and* lets the post-restart health probe connect to the stale listener and pass,
which previously made a doomed switch look successful. On the restart path only,
`Runner.Restart` therefore reclaims the port between the stop and the spawn
(`ReclaimPortOnRestart`, wired from `cmd/marquee`): it polls briefly for the port
to be released and, if it is still held, finds the PID(s) **listening on
marquee's own internal loopback port** via `lsof` (the same pattern as the
startup port diagnostic) and sends them `SIGKILL`, then confirms the port is
free before spawning the new child.

This is the only place marquee kills a process it did not itself spawn, so its
scope is deliberately tight and auditable:

- **What it kills:** only a process **listening on marquee's own internal
  loopback port** — the port marquee itself chose and handed the child as
  `PORT` — and only during a switch/restart, in the window after the old child's
  group is stopped and before the new child spawns. The port number is an
  integer marquee owns; it never comes from the HTTP request or the slug. PIDs
  come straight from `lsof` of that one port; pid `0`/`1` are never signalled.
- **Why:** the killed process is, by construction, an escaped remnant of the
  child marquee manages (a daemonized dev server holding the port marquee gave
  it), which the process-group stop cannot reach.
- **Logged:** each kill logs one line naming the PID and that it held the
  internal port — never a command line or any secret. The `lsof`/kill step
  carries a justified `#nosec G204` in `internal/runner/runner.go`.

Known limitation (accepted, narrowed by the above): a daemonizing manager still
leaves **sibling processes** in its detached session (e.g. `sidekiq`, `vite`)
and any stale manager socket (e.g. `.overmind.sock`) that marquee does not touch
— it reclaims only its own internal port, nothing else. Those may still need
operator cleanup, which the switch hook can do: for example
`--switch-hook="rm -f .overmind.sock && bundle install && pnpm install"` clears a
stale socket (which would otherwise make the manager refuse to start) and
installs dependencies before the target boots. The hook runs the **same cleanup
on the revert leg**, so a forward stop that stranded a stale socket in the
previous worktree no longer blocks the revert from bringing that worktree back
up. If the target still cannot come up, the switch takes the revert / both-fail
path **cleanly** (marquee stays alive, reports `switch_failed`, the user can
recover) rather than being fooled by the stale listener into a fake success that
later shuts marquee down.

Known limitation (accepted): a switch's managed window suppresses child-exit
shutdown until it closes. If a just-health-checked child dies in the sub-
millisecond tail between passing the health probe and the window closing on an
otherwise **successful** switch, that exit is suppressed and marquee keeps
running with the child down (the proxy serves its unavailable page; the user
can switch again or restart). The window is tiny and non-deterministic and the
degradation is graceful, so this is left as-is rather than complicating the
lifecycle guarantees that keep marquee alive across a failed switch.

**Token lifecycle.** The token is 256 bits from `crypto/rand`, hex-encoded (no
HTML-special characters, so it embeds without escaping), minted **once** at
startup and constant for the process. It reaches the browser **only** through
the injected `<marquee-bar token="…">` attribute on same-origin app pages —
never in the status JSON, any other GET response, or any log line (a failed
switch logs only the non-secret slug). If minting fails, marquee runs with no
token: the endpoint rejects every request and the bar hides its switcher, while
everything else keeps working (fail-open). Attach mode configures no token and
registers no switch endpoint. Golden fixtures stay valid because the token-less
snippet is byte-identical to before; the tokened case is asserted separately
with a fixed token, and no golden encodes the random value.

- Code: `internal/switcher/switcher.go` (whole file — `Handler.serve` running
  the hook before the child is stopped (a hook failure leaves the child
  untouched), `switchInto` for the restart→health→**live-child**→repoint step,
  `revertInto` for the single revert that re-runs the hook in the previous
  worktree, the `ChildAlive` check that fails a switch to an exited child,
  `runSwitchHook` for the operator hook run in the target — and, on a revert, the
  previous — worktree);
  `Runner.Terminated`, `Runner.BeginManaged`/`EndManaged`, the managed window in
  `Runner.Restart`, `Runner.ReclaimPortOnRestart` and `freeLoopbackPort` /
  `loopbackListeners` / `reapListener` (the kill-by-port reclaim), and the
  managed-aware wait goroutine in `internal/runner/runner.go`; the
  `child.Terminated()` select in `run`
  (`cmd/marquee/main.go`); `serveSwitching` / `renderSwitching` in
  `internal/proxy/switching.go`; `SetSwitchingProbe` / `switchingSlug` and
  `Config.SwitchToken` in `internal/proxy/proxy.go`; `barSnippetForToken` in
  `internal/proxy/inject.go`; `Poller.Repoint` in `internal/gitinfo/poller.go`
  and `internal/ghinfo/ghinfo.go`; the wiring (`mintToken`, `switcher.New`,
  `switcher.Register`, `SetSwitchingProbe`, `child.ReclaimPortOnRestart`,
  `ChildAlive`) in `cmd/marquee/main.go`.
- Proven by: the abuse and happy-path tests listed under Threats 3 and 4,
  plus `TestDirtyRefusedWithoutConfirm`, `TestDirtyConfirmedAllowed`,
  `TestDirtySwitchToMainAlwaysAllowed`, `TestConcurrentSwitchRejected`,
  `TestSwitchingSlugReportedWhileInProgress`,
  `TestHealthFailureRevertsToPreviousWorktree`,
  `TestSwitchFailsWhenChildDiesDespiteHealthOK` (a passing health probe on an
  exited child is a reverting failure, never a fake `ok:true`) in
  `internal/switcher/switcher_test.go`; the runner-level reclaim tests
  `TestFreeLoopbackPortFastWhenFree` and `TestFreeLoopbackPortReapsEscapedListener`
  (a Setsid listener squatting the internal port is reaped, the port ends up
  free, and exactly one non-secret reap line is logged) in
  `internal/runner/runner_test.go`; the real-runner integration tests
  `TestIntegrationHealthySwitchKeepsMarqueeUp`,
  `TestIntegrationFailedSwitchRevertsAndStaysAlive`,
  `TestIntegrationBothFailStaysAlive`,
  `TestIntegrationDaemonSelfKillRegression` (a daemonizing manager whose escaped
  listener holds the port while the target child exits: the switch is a reverting
  failure and `Terminated` never fires — marquee stays up),
  `TestIntegrationDaemonPortFreedSwitchSucceeds` (the escaped listener is reaped
  so the healthy target's new child binds the freed port and truly serves it),
  `TestIntegrationSwitchHookRunsInTargetWorktree` (the hook's marker lands in the
  target worktree and the child then boots there), `TestSwitchHookFailureLeavesChildUntouched`
  (a failing hook restarts nothing — not the target and not a bounce of the
  healthy child — and leaves the managed window balanced), `TestIntegrationFailingSwitchHookRevertsAndStaysAlive`
  (a failing hook reverts, stays alive, and never boots the target),
  `TestIntegrationSwitchHookRunsOnRevert` (the hook runs on both legs — forward
  and revert), `TestIntegrationRevertHookClearsStaleBlocker` (the revert hook is
  load-bearing: it clears a stale-socket-shaped blocker in the previous worktree
  so the revert recovers, reproducing the real incident) in
  `internal/switcher/integration_test.go` (each asserts the shutdown
  signal is or is not triggered against the real process lifecycle); the runner-level
  lifecycle tests `TestTerminatedFiresOnUnmanagedExit` (wrapper-mode regression),
  `TestTerminatedNotFiredDuringRestart` (`-race`),
  `TestManagedWindowSuppressesTerminatedForever`,
  `TestTerminatedFiresAfterRestartOnNaturalDeath` in
  `internal/runner/terminated_test.go`;
  `TestSwitchingPageServedWhileSwitching`, `TestSwitchingPageEscapesSlug`,
  `TestSwitchingProbeAbsentBehavesNormally` in
  `internal/proxy/switch_test.go`; `TestPollerRepointSwitchesDirectory`
  (`internal/gitinfo/poller_repoint_test.go`),
  `TestRepointChangesLookupDirectory` (`internal/ghinfo/ghinfo_repoint_test.go`).

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

## Deferred

- **Full teardown for daemonizing process managers.** marquee stops the child by
  killing its process group; a manager (e.g. `overmind`, a `tmux`-based runner)
  that daemonizes its server into a separate session can leave grandchildren
  holding the internal port across a switch. Today that fails cleanly via the
  revert / both-fail path (marquee stays alive); tracking and terminating
  out-of-group descendants is a deliberate follow-up, not built here. See the
  "Worktree switch endpoint" section.
