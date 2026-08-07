# marquee

`marquee -- bin/dev` starts your dev stack behind a transparent proxy on the port you already use, and every HTML page you load carries a small shadow-DOM bar telling you exactly which branch/worktree/PR you are looking at.

With git worktrees and coding agents in the mix, "which code is my browser actually showing?" is a real question. The answer belongs on the page, not in a terminal three windows away.

<!-- TODO(launch): demo GIF recorded against a small PUBLIC sample app — never an employer app -->

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/img/bar-expanded-dark.png">
  <img alt="The bar at the bottom of a dev app: a colored branch chip, a dirty-state dot, a worktree slug, and a PR link" src="docs/img/bar-expanded.png">
</picture>

Click it to collapse into a dot that tucks into the corner, out of your way:

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/img/bar-collapsed-dark.png">
  <img alt="The collapsed bar: a small dot in the bottom-left corner of the page" src="docs/img/bar-collapsed.png">
</picture>

**Status: pre-alpha, not released.**

The binary makes no network calls except to the upstream and (optionally, operator-visible) the local `gh` CLI. No telemetry, ever.

## What it does

- **Zero config.** `marquee -- bin/dev` and the port your browser already uses keeps working. marquee picks a free internal port, points your dev command's `PORT` at it, and serves your usual `:3000` itself. What it cannot infer, like the extra hostnames your app is reached by, goes in an optional [`.marquee/config`](#marqueeconfig-so-you-dont-pass-flags) so the command stays `marquee -- bin/dev`.
- **Injects a status bar into every HTML page** — a colored branch chip (color derived from the branch name), a dirty-state indicator, the worktree slug when you are not on the main worktree, and a link to the open PR when `gh` finds one.
- **Fully transparent.** Your app never knows marquee is there. The `Host` header is preserved (multi-tenant subdomain routing keeps working), WebSockets and Server-Sent Events pass through untouched, and any injection error falls open to the original bytes.
- **The bar lives in a shadow DOM** — your app's CSS can't restyle it and it can't leak into your app. Accessible by design: a `role="status"` landmark, real keyboard-operable buttons, and text that clears 4.5:1 contrast in both light and dark themes.
- **No dependencies.** A single Go binary. No browser extension, no separate frontend, nothing to run alongside it.

## Install

Install with Homebrew:

```sh
brew install jellehuibregtse/tap/marquee
```

Or build from source:

```sh
git clone https://github.com/jellehuibregtse/marquee
cd marquee
go build ./cmd/marquee
```

Once the repo is public, `go install github.com/jellehuibregtse/marquee/cmd/marquee@latest` will work too.

Requires Go 1.22+. macOS and Linux only (no Windows in v1).

## Usage

Wrapper mode is the default — marquee spawns your dev command with `PORT` pointed at an internal port and serves your usual port itself:

```sh
marquee -- <your dev command>
# e.g.
marquee -- bin/dev
```

Everything after `--` is your command, run verbatim.

Flags a repo always needs belong in [`.marquee/config`](#marqueeconfig-so-you-dont-pass-flags) rather than in your muscle memory.

### Flags

| Flag | Default | What it does |
|---|---|---|
| `--listen` | `127.0.0.1:3000` | Address marquee listens on. Loopback only. |
| `--internal-port` | `0` (free port) | Port the child binds to; marquee sets `PORT` to this. |
| `--position` | `bottom-left` | Which corner the bar sits in: `bottom-left`, `bottom-right`, `top-left`, `top-right`. |
| `--size` | `medium` | Bar size preset: `small`, `medium`, `large`. |
| `--theme` | `default` | Bar theme: `default` (matches your light/dark scheme), `midnight`, `sand`, `forest`. |
| `--pills` | `branch,dirty,worktree,pr` | Which info pills to show and in what order (comma-separated). Omit an id to hide it; empty hides all. |
| `--open` | off | Open the browser once the app is up. Off by default: marquee is usually left running all day under a process manager, and a window it opens by itself is one nobody asked for. |
| `--quiet` | off | Suppress marquee's own log lines. |
| `--allow-host` | — | Add a hostname to the internal-endpoint allowlist (repeatable). |
| `--switch-hook` | `.marquee/hook` | Command run in a worktree before the child starts there, e.g. `"bundle install"`, including the worktree marquee itself is launched in. Bootstraps a fresh worktree; defaults to `.marquee/hook` when that is executable, and an empty value turns it off. See [Bootstrapping a worktree](#bootstrapping-a-worktree-switch-hook). |
| `--hook-timeout` | `60m` | Absolute ceiling on one `--switch-hook` run before marquee kills its process group. A hook that has printed nothing for `--hook-idle-timeout` is killed long before this; see [Bootstrapping a worktree](#bootstrapping-a-worktree-switch-hook). |
| `--hook-idle-timeout` | `10m` | How long a `--switch-hook` run may print nothing at all before marquee kills its process group. Raise it if your bootstrap has a step that legitimately goes quiet for longer; it must not exceed `--hook-timeout`. |
| `--worktree-glob` | — | Only offer worktrees whose absolute path matches this glob as switch targets (repeatable). See [Switching worktrees](#switching-worktrees). |
| `--ready-cmd` | — | Extra readiness check the target must pass before a switch counts as a success, e.g. `"curl -sf localhost:3036"`. See [Switching worktrees](#switching-worktrees). |
| `--health-timeout` | `30s` | How long to wait for the restarted child's port, and separately for `--ready-cmd`, before the switch reverts. |
| `--restart-timeout` | `30s` | How long a single stop-and-spawn of the child may take. |
| `--unsafe-listen` | off | Allow a non-loopback `--listen` address. Prints a persistent warning; exposes your dev app to the network. |

### Customizing the bar

`--position`, `--size`, `--theme`, and `--pills` set the starting defaults. You can also change all four live from the **⚙ settings panel** in the bar itself — click the gear, pick a corner/size/theme, or toggle and reorder pills. Panel choices are saved in the browser per app and win over the flags on the next load; **Reset** returns everything to the flag defaults.

### `.marquee/config`, so you don't pass flags

Anything in the flag table above can live in `.marquee/config` in the directory you launch marquee in, and then the invocation is just `marquee -- bin/dev` again:

```
# .marquee/config
--allow-host '*.example.test'   # our dev app is reached by subdomain
--position top-right
```

The file holds **the flags you would have typed**: one per line, `#` starts a comment, blank lines are ignored, and quoting works the way a shell's does, so a value with a space or a `*` in it needs no escaping. There is no second set of key names to learn and nothing that can drift from the flags themselves.

Precedence follows from that. The file's flags are parsed as if they came first on the command line, so:

- **the command line wins** for a flag that holds one value: `--position bottom-left` on the command line beats `--position top-right` in the file;
- **repeatable flags add up**: `--allow-host` and `--worktree-glob` given on the command line extend what the file listed rather than replacing it, because "also allow this host" is the only thing that flag has ever meant;
- **an invalid flag in the file fails the run**, naming the file and the line it is on:

  ```
  marquee: /path/to/repo/.marquee/config:3: flag provided but not defined: -no-open
  ```

  which is the message you want when a flag you wrote once has since been renamed or removed, rather than a complaint about a command line that never mentioned it.

Two things the file deliberately cannot do:

- **It cannot choose what marquee runs.** `--` and the command after it come from the command line only. A `--`, or any bare word, in the file is an error rather than a command, so a config file can never turn into a process. It also means the file is safe to commit, and `marquee -- bin/dev` still says on screen what is about to run.
- **It cannot be per worktree.** Like the hook, the file is read once from the launch checkout, so switching worktrees never changes the flags underneath a running marquee.

A missing file is the normal case and says nothing. A file that exists but cannot be read, or that marquee cannot parse, fails the run and names the line: it was written to change how marquee behaves, so starting with none of it applied is the one outcome to avoid.

Attach mode does not read it. Its flag set is a different one, and a file written for `marquee -- bin/dev` would fail `marquee attach` over flags attach does not have.

### Bootstrapping a worktree (`--switch-hook`)

A fresh git worktree is usually not ready to run: dependencies are missing, there is no local env file, the database it wants doesn't exist yet. `--switch-hook` is the command that makes a worktree runnable, and marquee runs it in a worktree **before it starts your dev command there**:

```sh
marquee --switch-hook "bin/setup" -- bin/dev
```

That covers every start, including the very first one:

- **on startup**, in the worktree you launched marquee in;
- **on a switch**, in the worktree you are switching to;
- **on a revert**, in the worktree marquee is falling back to after a failed switch (a cleanup step like `rm -f .overmind.sock` is what lets a process manager boot there again).

Running it on startup is the point: the worktree you happen to launch in is a worktree like any other, and if it were the one marquee never bootstrapped, it would be the one silently running against whatever a half-configured environment points at.

The hook runs through `sh -c` with its working directory set to the worktree, so pipelines and `&&` chains work. **Write it to be idempotent**: the revert leg re-runs it in a worktree that was already working, and every restart runs it again.

#### `.marquee/hook`, so you don't pass the flag

You don't need the flag at all. If `.marquee/hook` in the directory you launch marquee in is an executable file, that file *is* the hook:

```sh
ln -s "$PWD/bin/setup" .marquee/hook   # or write the script there directly
marquee -- bin/dev
```

Symlinks are followed, so pointing it at a script the repo already keeps (including one in a gitignored directory) is the expected way to adopt it.

It is resolved **once, in the launch checkout**, and marquee runs that absolute path in each worktree it bootstraps. Resolving it per worktree would be the wrong thing: the worktrees you switch between are usually throwaway checkouts that never had the file, and a hook that disappears halfway through a session is worse than one that was never there.

- An explicit `--switch-hook` wins over the file.
- `--switch-hook ''` ignores the file, which is how you turn the convention off without deleting anything.
- Only an **executable regular file** counts. Anything else there (a directory, a dangling symlink, a script you forgot to `chmod +x`) is named in a warning at startup and not run. A bootstrap you believe happened but didn't is worse than a loud refusal, so marquee never stays quiet about a hook it declined to run.

One script can serve all three legs, because marquee tells it what is going on through the environment:

| Variable | What it holds |
|---|---|
| `MARQUEE_HOOK_LEG` | `start`, `switch`, or `revert`: which of the three legs above this run is. |
| `MARQUEE_TARGET_SLUG` | The worktree being bootstrapped, named as `git worktree list` names it (the directory's own name). |
| `MARQUEE_TARGET_DIR` | Absolute path marquee starts the child in, which is also the hook's working directory. Normally the worktree root, but if you launched marquee from a subdirectory of it, that subdirectory (the child gets the same one). |
| `MARQUEE_PREV_DIR` | Absolute path of the worktree the child was running in. **Empty on `start`**, since nothing was running yet. |

So a hook that only wants to do the expensive setup for a worktree it has not seen before can key off `$MARQUEE_TARGET_SLUG`, and one whose only job is cleaning up after a failed switch can do nothing unless `$MARQUEE_HOOK_LEG` is `revert`:

```sh
marquee --switch-hook 'test "$MARQUEE_HOOK_LEG" = revert && rm -f .overmind.sock; bin/setup "$MARQUEE_TARGET_SLUG"' -- overmind start
```

A hook that has printed **nothing for 10 minutes** is killed, process group and all. That bound is not a judgement about whether your bootstrap is making progress, because nothing marquee can see tells it that: a step that normally finishes in seconds can go quiet for minutes and then finish fine. It is there so a hook that will never finish (a stalled fetch, a lock, a prompt waiting for an answer you cannot give) cannot hold the worktree forever, so it is set generously. A hook that keeps printing is left alone however long it takes, so a cold dependency install building native extensions is not cut off for being slow. How long your own bootstrap can go quiet is something only your repo knows, so `--hook-idle-timeout` changes it, and `.marquee/config` is the place to put it once instead of on every invocation. `--hook-timeout` is only the outer ceiling on one run (60 minutes), high enough that it should never fire; when a hook is killed, the error says which of the two it was.

A failing hook (a non-zero exit, or either of those kills) never leaves you with a half-switched app:

- **at startup** marquee refuses to start your dev command and exits non-zero;
- **on a switch** the hook runs before the current child is stopped, so a failure fails the switch and leaves your running app untouched;
- **on a revert** the failure is logged and the restart is attempted anyway, since that worktree booted once already.

The hook's output is streamed as it runs, which `--quiet` suppresses along with marquee's other progress lines. The last lines of a *failing* hook are printed again as an error, so `--quiet` never hides why a bootstrap failed.

While the hook runs at startup, marquee is already serving: a browser that arrives mid-bootstrap gets the same self-refreshing "app is starting" page it gets while your dev server boots. Ctrl-C stops the hook and its child processes, whether it is bootstrapping at startup or in the middle of a switch.

### Switching worktrees

The bar's picker repoints marquee at another worktree of the same repo: it bootstraps the target as described above, restarts your dev command there, waits for the target to come up, and reverts to where you were if it doesn't. The flags below cover the rest of that: whether the target counts as healthy, and which worktrees you can switch into at all.

Once the child has restarted, marquee waits for it to accept a connection on its internal port. That is all a TCP check can tell you, and with a process manager it is not the whole stack: the asset server and the workers have their own fixed ports, and a remnant of the old stack still holding one of them looks fine from the web port. `--ready-cmd` is the check for that. It runs in the target worktree, retried every 500ms until it exits 0 or `--health-timeout` expires:

```sh
marquee --ready-cmd "curl -sf localhost:3036 && curl -sf localhost:7433/health" -- bin/dev
```

The early attempts are expected to fail while things come up, so its output is not streamed; you see it only if the check never passes. `--health-timeout` applies to the port wait and to the readiness command separately, so a slow but successful port wait can't leave the readiness check no time to pass. The revert after a failed switch is not gated on `--ready-cmd`, since that leg only has to get your dev server back up. `--restart-timeout` bounds one stop-and-spawn of the child, which is the step before either check.

The picker lists the main worktree first, then every other worktree by branch recency, most recently committed at the top. With dozens of worktrees that puts the two or three you are working in at the top of the menu, and the search box finds any of the rest. A worktree with a detached HEAD has no branch to date it, so it sorts last.

`--worktree-glob` decides which worktrees are offered at all, matched against the worktree's absolute path:

```sh
marquee --worktree-glob '/Users/me/code/wt/*' -- bin/dev
```

With no glob, every worktree git knows about is a target. Repeat the flag to keep several places. The main worktree is always a target, whatever the globs say, because switching back to it is how you get out of a dirty or broken worktree. marquee logs the surviving target set at startup whenever you pass a glob, and warns when nothing but the main worktree survived, because three things trip people up:

- **`*` never crosses a `/`.** `/path/*` matches `/path/one` but not `/path/one/two`, and `/path/**` matches exactly the same set as `/path/*` (it reads as recursive; it isn't). Point the glob one level up from the worktrees you mean.
- **Match the path git reports, which is the resolved one.** On macOS a worktree you think of as `/tmp/wt/feature` is `/private/tmp/wt/feature` to git, and a glob under `/tmp/...` never matches it.
- **Matching is case-sensitive**, even on a case-insensitive filesystem like the macOS default.

One consequence worth knowing: the bar only shows its switcher when more than one worktree survives filtering, so globs that match nothing leave you with no switcher at all. Switching back to main stays possible over the API either way.

### From the terminal

Everything the bar shows and the picker does is also two subcommands, so a switch doesn't need a browser:

```sh
marquee status
marquee switch feature
```

Neither takes an address. marquee records where it listens in a file under your user cache directory when it starts, and removes it when it exits, so `status` and `switch` find the marquee serving the repository you are standing in. Run several marquees for several projects and each command still picks the right one, because it matches your working directory against the worktrees that marquee knows about. If two of them could both be meant, the command says so and asks for `--listen` rather than guessing, since a switch restarts your dev server.

`status` prints the branch, the current worktree, the pull request, the child's state, and the worktrees you can switch into, with the current one marked. `switch` takes a worktree slug, which is the directory name git reports and the same name `status` lists. A typo gets you the real list back:

```
$ marquee switch featrue
marquee: unknown worktree "featrue"; switch targets are: main, feature, hotfix
```

A switch runs your `--switch-hook` in the target worktree, so the command blocks for as long as that takes. There is no client-side timeout; the server's `--hook-timeout` and `--health-timeout` are still the only limits. The dirty-worktree rule is the picker's rule too, so switching out of a worktree with uncommitted changes needs `--force`, and switching back to main never does.

Both take `--json`, which is what makes them scriptable. `marquee status --json` is the status endpoint's own payload passed through unchanged, so a field the human summary doesn't render is still there for `jq`. `marquee switch --json` prints the switch response, whose `error` field is `dirty`, `busy`, `unknown_slug`, or `switch_failed`. Exit status is 0 for success, 2 for a usage mistake, 1 for everything else.

There is nothing extra to grant: both are clients of the same HTTP endpoints the bar uses and pass the same guards.

### Attach mode

For a server marquee shouldn't manage — one you start yourself in another terminal — use attach mode. It's a pure proxy with no child process:

```sh
# your server, started however you like, on :3100
marquee attach --listen 127.0.0.1:3000 --upstream http://localhost:3100
```

Both `--listen` and `--upstream` must be loopback; a non-loopback value is refused unless you pass `--unsafe-listen` (which prints a persistent network-exposure warning). `--upstream` is required and must be an `http`/`https` URL.

Attach mode has no child process to restart, so there is nothing to switch and it records no session file. `marquee status --listen 127.0.0.1:3000` still reports what the bar shows; `marquee switch` says why it can't.

## The PORT recipe (process managers)

marquee's whole trick is the `PORT` environment variable. When it spawns your dev command it sets `PORT` to its internal port (plus `MARQUEE=1` and `MARQUEE_PORT`), then proxies your real port to it. All you have to do is make your web server bind `$PORT`.

For a bare server that already honors `PORT`, there's nothing to do:

```sh
marquee -- npm run dev        # if the dev script binds process.env.PORT
marquee -- bin/rails server   # Rails' default puma.rb reads ENV["PORT"]
```

If you use a process manager with a `Procfile`, the child processes inherit marquee's environment, so make your web process bind `$PORT` explicitly:

```procfile
# Procfile
web: bin/server -p $PORT
```

**foreman:**

```sh
marquee -- foreman start
```

foreman passes its own environment through to each process, so `web: bin/server -p $PORT` picks up the `PORT` marquee set.

**overmind:**

```sh
marquee -- overmind start
```

By default overmind assigns each process its own incrementing `PORT`, which would override marquee's. Tell it not to:

```sh
marquee -- env OVERMIND_NO_PORT=1 overmind start
```

With `OVERMIND_NO_PORT=1`, overmind leaves `PORT` alone and your `web` process inherits the value marquee set.

Only the web process's port matters. A separate asset/HMR server (Vite and similar) serves on its own port that the page references directly — those requests bypass marquee entirely, which is correct; marquee only needs the HTML document.

**Variables that differ per worktree.** marquee hands the child its own environment on every start and restart, so anything exported in the shell you launched marquee from follows you into every worktree you switch into. A value that must differ per worktree therefore has to come from somewhere that beats an inherited one, and whether your loader does that is worth checking: overmind's `.env` wins over an exported variable (by default it reads `./.env` and `./.overmind.env`; `-e` or `OVERMIND_ENV_FILE` points it elsewhere), while dotenv-style loaders leave a variable that is already set alone. With a dotenv-style loader, `export DATABASE_NAME=…` typed once in your shell silently pins every worktree to that database, whatever each worktree's own env file says.

## FAQ

### Why is my bar missing?

marquee injects the bar only when **all** of these hold for a response:

- Status is **2xx**.
- `Content-Type` starts with **`text/html`**.
- It is **not an event stream** (Server-Sent Events are passed through so they keep streaming).
- The body is **identity-encoded** from the app's perspective — marquee forces `Accept-Encoding: identity` on upstream requests so it never has to touch gzip/brotli.
- The body is under the **size cap** (~10 MB; larger documents pass through untouched).
- The body is a **full document** — it must contain a `</body>` (searched case-insensitively from the end). Turbo-frame and other partial responses lack `</body>` and are skipped naturally. Streamed HTML with no buffered `</body>` is passed through untouched too.

Framed documents are also skipped (one bar per page, not one per iframe), and the bar script refuses to render when it isn't the top-level window.

If your app sends a **Content-Security-Policy**, the bar needs `'self'` allowed for its own script (`/__marquee/bar.js`) and fetch (`/__marquee/status`). By default marquee handles this for you: whenever it injects the bar, it rewrites that response's enforcing CSP to add `'self'` to the script- and connect-governing directives, and nothing else — report-only CSP and every other directive are left untouched, and only same-origin `'self'` is ever granted (never a third party). Pass `--keep-csp` to leave your app's CSP exactly as it sent it. Relaxation only rewrites the response **header**, so a CSP delivered inside the page via a `<meta http-equiv="Content-Security-Policy">` tag, or a CSP on a route marquee doesn't inject into, can still keep the bar from loading. See [docs/security.md](docs/security.md) for exactly what changes.

### Does it change my app's responses?

No. Injection is a byte-splice of `<script>` + `<marquee-bar>` immediately before the final `</body>`, and nothing else. The `Host` header is preserved verbatim, `Content-Length` is recomputed so the page never truncates, and every injection path is **fail-open**: any error at any point writes the original bytes through and logs once. Non-HTML responses (JSON, assets, downloads) stream through unmodified.

### Can I turn the bar off?

Yes — three switches, all of which stop injection while proxying continues:

- **Per request:** send the header `X-Marquee: skip` (set it once in a Playwright fixture or a Capybara driver that supports custom headers). It is stripped before the request reaches your app.
- **Per run:** launch with `MARQUEE_DISABLE_BAR=1`. This is a hard off the runtime toggle cannot re-enable.
- **Mid-session:** open `/__marquee/toggle?bar=off` in the address bar (`?bar=on` flips it back; no parameter reports the current state).

This matters for browser automation — screenshots, visual regression, automated QA runs — where an unexpected fixed bar pollutes results.

## Security

marquee sits in a trusted spot: it terminates all your browser traffic to the app and injects script into every page. It is built accordingly. See [docs/security.md](docs/security.md) for the full model; in short:

- **Loopback only.** marquee refuses to listen on a non-loopback address. Exposing it to the network requires the explicit `--unsafe-listen` opt-in, which prints a persistent warning that `--quiet` cannot suppress; attach mode refuses non-loopback upstreams the same way.
- **The `/__marquee/*` endpoints are Host-allowlisted** (`localhost`, `127.0.0.1`, `::1`, `*.localhost`, plus any `--allow-host` additions) to defeat DNS rebinding, and every response is `Cache-Control: no-store`. `lvh.me` is a third-party wildcard DNS domain and is no longer trusted by default; opt back in with `--allow-host '*.lvh.me'` (the flag accepts exact hosts and `*.<suffix>` wildcards). The deliberate asymmetry: proxied app traffic keeps its `Host` untouched so multi-tenant routing works; only marquee's own endpoints validate.
- **Injection is byte-splicing only** — no HTML parsing, no `eval`, no external or third-party scripts ever. The bar script only fetches same-origin `/__marquee/` URLs.
- **The v2 worktree-switch endpoint** — which will kill and spawn processes — is guarded on top of the above by a same-origin check plus a per-process random token minted at startup and echoed by the injected bar. A random web page must not be able to make your proxy restart your app.
- **Zero runtime dependencies**, which keeps the supply-chain surface minimal.

## Non-goals

marquee is a local dev tool, not infrastructure. It is deliberately **not**:

- a process-manager replacement (overmind/foreman keep their jobs; marquee wraps them);
- a tunneling or HTTPS/TLS tool (plain HTTP on localhost only);
- extensively configurable: flags are the whole interface, and the optional `.marquee/config` is those same flags kept in a file rather than a settings schema of its own;
- production-safe — it refuses to start unless the upstream looks local, and never will be.
