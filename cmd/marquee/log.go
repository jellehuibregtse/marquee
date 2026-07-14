package main

import (
	"fmt"
	"io"
)

// logger splits marquee's own output into informational lines, which
// --quiet suppresses, and warnings/errors, which always print. Child
// process output flows through the runner's inherited stdio and never
// passes through here, so no flag can touch it.
type logger struct {
	out   io.Writer
	quiet bool
}

func newLogger(out io.Writer, quiet bool) *logger {
	return &logger{out: out, quiet: quiet}
}

// Info prints an operational status line, suppressed under --quiet.
func (l *logger) Info(format string, args ...any) {
	if l.quiet {
		return
	}
	l.line(format, args...)
}

// Warn prints a warning; --quiet never suppresses it.
func (l *logger) Warn(format string, args ...any) {
	l.line("warning: "+format, args...)
}

// Error prints an error; --quiet never suppresses it.
func (l *logger) Error(format string, args ...any) {
	l.line(format, args...)
}

func (l *logger) line(format string, args ...any) {
	_, _ = fmt.Fprintf(l.out, "marquee: "+format+"\n", args...)
}
