package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// runStatus implements the status subcommand: what the bar shows, in a
// terminal. It reads the same GET /__marquee/status the bar polls, which is
// Host-guarded but unauthenticated, so it works against `marquee attach` too.
func runStatus(args []string) int {
	fs := flag.NewFlagSet("marquee status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	listen := fs.String("listen", "", listenTargetUsage)
	asJSON := fs.Bool("json", false, "print the status endpoint's payload instead of the human summary")
	fs.Usage = func() {
		_, _ = fmt.Fprint(os.Stderr, "usage: marquee status [--listen addr] [--json]\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(os.Stderr, "marquee status: unexpected argument %q\n", fs.Arg(0))
		return 2
	}

	t, err := resolveTarget(*listen)
	if err != nil {
		reportTargetError(os.Stderr, err)
		return 1
	}

	if *asJSON {
		_, _ = os.Stdout.Write(t.raw)
		if !strings.HasSuffix(string(t.raw), "\n") {
			_, _ = fmt.Fprintln(os.Stdout)
		}
		return 0
	}
	printStatus(os.Stdout, t)
	return 0
}

func printStatus(w io.Writer, t target) {
	s := t.status
	_, _ = fmt.Fprintf(w, "marquee on %s\n", t.listen)
	_, _ = fmt.Fprintf(w, "  worktree  %s  %s\n", s.Worktree.Slug, s.Worktree.Path)
	branch := s.Branch
	if s.Dirty {
		branch += " (dirty)"
	}
	_, _ = fmt.Fprintf(w, "  branch    %s\n", branch)
	if s.PR != nil {
		_, _ = fmt.Fprintf(w, "  pr        #%d %s\n", s.PR.Number, s.PR.Title)
	}
	if s.Child.State != "" {
		_, _ = fmt.Fprintf(w, "  child     %s\n", s.Child.State)
	}
	if len(s.Worktrees) == 0 {
		return
	}
	_, _ = fmt.Fprintf(w, "\nswitch targets (* is current):\n")
	width := 0
	for _, wt := range s.Worktrees {
		if len(wt.Slug) > width {
			width = len(wt.Slug)
		}
	}
	for _, wt := range s.Worktrees {
		marker := " "
		if wt.Slug == s.Worktree.Slug {
			marker = "*"
		}
		_, _ = fmt.Fprintf(w, "%s %-*s  %s\n", marker, width, wt.Slug, wt.Branch)
	}
}

// listenTargetUsage documents the flag both client subcommands share. The
// default is discovery rather than an address, because the port a marquee
// listens on is exactly what someone running this from a worktree does not want
// to have to remember.
const listenTargetUsage = "address of the marquee to talk to; the default finds the one running for this repository"

// reportTargetError turns a failed lookup into advice. "Nothing is running" is
// the overwhelmingly common case and deserves the obvious next step rather than
// a bare error.
func reportTargetError(w io.Writer, err error) {
	if errors.Is(err, errNoMarquee) {
		_, _ = fmt.Fprintln(w, "marquee: no marquee is running; start one with: marquee -- <your dev server command>")
		return
	}
	_, _ = fmt.Fprintf(w, "marquee: %v\n", err)
}
