package ghinfo

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const canned = `{"number":42,"title":"Fix double submit","url":"https://github.com/acme/widgets/pull/42"}`

func fakeGH(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

type logRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *logRecorder) logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

func (r *logRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.lines)
}

func (r *logRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func newTestPoller(t *testing.T, executable string, opts ...Option) *Poller {
	t.Helper()
	opts = append([]Option{
		WithExecutable(executable),
		WithInterval(10 * time.Millisecond),
		WithTimeout(time.Second),
	}, opts...)
	p := New(t.TempDir(), opts...)
	t.Cleanup(p.Stop)
	return p
}

func TestHappyParse(t *testing.T) {
	p := newTestPoller(t, fakeGH(t, "echo '"+canned+"'"))
	waitFor(t, "PR to populate", func() bool { return p.PR() != nil })
	pr := p.PR()
	if pr.Number != 42 || pr.Title != "Fix double submit" || pr.URL != "https://github.com/acme/widgets/pull/42" {
		t.Fatalf("unexpected PR: %+v", pr)
	}
}

func TestMissingBinary(t *testing.T) {
	rec := &logRecorder{}
	p := newTestPoller(t, filepath.Join(t.TempDir(), "no-such-gh"), WithLogger(rec.logf))
	waitFor(t, "failure log", func() bool { return rec.count() > 0 })
	if pr := p.PR(); pr != nil {
		t.Fatalf("expected nil PR, got %+v", pr)
	}
}

func TestNonZeroExit(t *testing.T) {
	rec := &logRecorder{}
	p := newTestPoller(t, fakeGH(t, "exit 1"), WithLogger(rec.logf))
	waitFor(t, "failure log", func() bool { return rec.count() > 0 })
	if pr := p.PR(); pr != nil {
		t.Fatalf("expected nil PR, got %+v", pr)
	}
}

func TestMalformedJSON(t *testing.T) {
	rec := &logRecorder{}
	p := newTestPoller(t, fakeGH(t, "echo 'this is not json'"), WithLogger(rec.logf))
	waitFor(t, "failure log", func() bool { return rec.count() > 0 })
	if pr := p.PR(); pr != nil {
		t.Fatalf("expected nil PR, got %+v", pr)
	}
}

func TestTimeout(t *testing.T) {
	rec := &logRecorder{}
	p := newTestPoller(t, fakeGH(t, "sleep 5"), WithLogger(rec.logf), WithTimeout(30*time.Millisecond))
	waitFor(t, "timeout log", func() bool { return rec.count() > 0 })
	if pr := p.PR(); pr != nil {
		t.Fatalf("expected nil PR, got %+v", pr)
	}
}

func TestAsyncFirstPopulate(t *testing.T) {
	start := time.Now()
	p := newTestPoller(t, fakeGH(t, "sleep 0.2; echo '"+canned+"'"))
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("New blocked for %v", elapsed)
	}
	if pr := p.PR(); pr != nil {
		t.Fatalf("expected nil PR before first fetch, got %+v", pr)
	}
	waitFor(t, "PR to populate", func() bool { return p.PR() != nil })
}

func TestLogOnce(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "calls")
	rec := &logRecorder{}
	p := newTestPoller(t, fakeGH(t, `echo x >> "`+counter+`"; exit 1`), WithLogger(rec.logf))
	waitFor(t, "three gh invocations", func() bool {
		data, err := os.ReadFile(counter)
		return err == nil && strings.Count(string(data), "x") >= 3
	})
	if lines := rec.snapshot(); len(lines) != 1 {
		t.Fatalf("expected exactly 1 log line for a repeated failure, got %d: %v", len(lines), lines)
	}
	p.Stop()
}

func TestStopHaltsLoop(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "calls")
	p := newTestPoller(t, fakeGH(t, `echo x >> "`+counter+`"; exit 1`))
	waitFor(t, "first gh invocation", func() bool {
		_, err := os.Stat(counter)
		return err == nil
	})
	p.Stop()
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	before := strings.Count(string(data), "x")
	time.Sleep(50 * time.Millisecond)
	data, err = os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	if after := strings.Count(string(data), "x"); after != before {
		t.Fatalf("poller kept running after Stop: %d invocations before, %d after", before, after)
	}
}
