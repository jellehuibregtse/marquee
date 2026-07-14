# marquee

`marquee -- bin/dev` starts your dev stack behind a transparent proxy on the port you already use, and every HTML page you load carries a small shadow-DOM bar telling you exactly which branch/worktree/PR you are looking at.

**Status: pre-alpha, not released.**

The binary makes no network calls except to the upstream and (optionally, operator-visible) the local `gh` CLI.

## Install

Not yet published. Build from source with `go build ./cmd/marquee` for now.

## Usage

Wrapper mode (default) — marquee spawns your dev command with `PORT` pointed at an internal port and serves your usual port itself:

```sh
# e.g. a Rails app whose Procfile.dev runs `web: bundle exec rails s`
marquee -- bin/dev
```

Attach mode — pure proxy in front of a server you manage yourself:

```sh
marquee attach --listen 127.0.0.1:4000 --upstream http://localhost:3000
```

Both `--listen` and `--upstream` must be loopback; a non-loopback value is refused unless you pass `--unsafe-listen` (which prints a persistent network-exposure warning). `--upstream` is required and must be an `http`/`https` URL.

## Disabling the bar (CI / screenshots / automation)

Browser automation wants pristine pages — an unexpected fixed bar pollutes screenshots and can trip full-page assertions. Three switches turn injection off while proxying continues untouched:

- **Per request:** send the header `X-Marquee: skip`. Set it once in a Playwright fixture (`use: { extraHTTPHeaders: { "X-Marquee": "skip" } }` in `playwright.config.ts`) or a Capybara driver that supports custom headers (e.g. Cuprite) and every page in the run comes back clean. The header is stripped before the request reaches your app.
- **Per run:** launch with `MARQUEE_DISABLE_BAR=1 marquee -- bin/dev` and the bar stays off for the whole process — the toggle below cannot re-enable it.
- **Mid-session:** open `/__marquee/toggle?bar=off` in the address bar to turn the bar off for everyone until `?bar=on` flips it back; without a parameter it reports the current state.

## FAQ
