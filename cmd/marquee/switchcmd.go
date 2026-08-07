package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// runSwitch implements the switch subcommand: the worktree switch the bar's
// menu performs, from a terminal or a script. It is a client of the existing
// POST /__marquee/switch and adds no guard of its own — the endpoint's stack
// (same-origin, token, busy lock, strict slug, dirty safety) decides, exactly
// as it does for the bar.
func runSwitch(args []string) int {
	fs := flag.NewFlagSet("marquee switch", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	listen := fs.String("listen", "", listenTargetUsage)
	force := fs.Bool("force", false, "switch even though the current worktree has uncommitted changes")
	asJSON := fs.Bool("json", false, "print the switch endpoint's response instead of the human summary")
	fs.Usage = func() {
		_, _ = fmt.Fprint(os.Stderr, "usage: marquee switch <worktree> [--force] [--listen addr] [--json]\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	slug := fs.Arg(0)

	t, err := resolveTarget(*listen)
	if err != nil {
		reportTargetError(os.Stderr, err)
		return 1
	}
	if t.token == "" {
		_, _ = fmt.Fprintf(os.Stderr, "marquee: the marquee on %s has no switch token, so it cannot switch worktrees; `marquee attach` mode has no child process to restart\n", t.listen)
		return 1
	}
	if err := checkSlug(t, slug); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "marquee: %v\n", err)
		return 1
	}
	if slug == t.status.Worktree.Slug {
		_, _ = fmt.Fprintf(os.Stderr, "marquee: already on %s\n", slug)
		return 0
	}

	_, _ = fmt.Fprintf(os.Stderr, "marquee: switching to %s, this takes as long as your switch-hook does\n", slug)
	result, raw, err := postSwitch(t, slug, *force)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "marquee: %v\n", err)
		return 1
	}
	if *asJSON {
		_, _ = os.Stdout.Write(raw)
		if !strings.HasSuffix(string(raw), "\n") {
			_, _ = fmt.Fprintln(os.Stdout)
		}
	}
	if result.Error == "" && result.OK {
		if !*asJSON {
			_, _ = fmt.Fprintf(os.Stdout, "switched to %s  %s\n", result.Slug, result.Path)
		}
		return 0
	}
	if !*asJSON {
		reportSwitchFailure(os.Stderr, result)
	}
	return 1
}

// checkSlug rejects an unknown worktree before the request goes out, purely so
// the message can list the real ones — a typo is the likeliest way this command
// is used wrong, and "unknown_slug" alone leaves the caller guessing. The
// endpoint validates the slug again on its own; this never becomes the
// authority.
func checkSlug(t target, slug string) error {
	slugs := make([]string, 0, len(t.status.Worktrees))
	for _, wt := range t.status.Worktrees {
		if wt.Slug == slug {
			return nil
		}
		slugs = append(slugs, wt.Slug)
	}
	if len(slugs) == 0 {
		return fmt.Errorf("the marquee on %s reports no switch targets", t.listen)
	}
	return fmt.Errorf("unknown worktree %q; switch targets are: %s", slug, strings.Join(slugs, ", "))
}

// reportSwitchFailure renders the endpoint's machine-readable error as the
// sentence the operator needs, keeping the two that have an obvious next move
// (dirty, busy) out of the generic path.
func reportSwitchFailure(w io.Writer, result switchResult) {
	switch result.Error {
	case "dirty":
		_, _ = fmt.Fprintln(w, "marquee: the current worktree has uncommitted changes; pass --force to switch anyway")
	case "busy":
		_, _ = fmt.Fprintln(w, "marquee: another switch is already in progress")
	default:
		_, _ = fmt.Fprintf(w, "marquee: %s\n", result.Message)
	}
}
