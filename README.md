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
marquee attach --listen 4000 --upstream http://localhost:3000
```

## FAQ
