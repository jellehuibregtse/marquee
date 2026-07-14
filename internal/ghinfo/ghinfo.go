package ghinfo

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os/exec"
	"sync"
	"time"
)

// PR describes the open pull request for the current branch, as reported by
// the `gh` CLI. Field tags match the `pr` object in the status endpoint.
type PR struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
}

// Poller periodically looks up the current PR via `gh pr view` and caches the
// result. Every failure mode (missing binary, unauthenticated, no PR, bad
// output, timeout) yields a nil PR: the feature is silently absent.
type Poller struct {
	dir        string
	executable string
	interval   time.Duration
	timeout    time.Duration
	logf       func(format string, args ...any)

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once

	mu       sync.Mutex
	pr       *PR
	lastFail string
}

// Option configures a Poller.
type Option func(*Poller)

// WithInterval sets the refresh interval (default 60s).
func WithInterval(d time.Duration) Option {
	return func(p *Poller) { p.interval = d }
}

// WithTimeout sets the per-invocation timeout for the gh command (default 3s).
func WithTimeout(d time.Duration) Option {
	return func(p *Poller) { p.timeout = d }
}

// WithExecutable overrides the gh executable name or path (default "gh").
func WithExecutable(name string) Option {
	return func(p *Poller) { p.executable = name }
}

// WithLogger overrides the logging function (default log.Printf).
func WithLogger(logf func(format string, args ...any)) Option {
	return func(p *Poller) { p.logf = logf }
}

// New creates a Poller for the given working directory and starts refreshing
// in the background. The first populate is asynchronous: PR returns nil until
// the initial lookup completes.
func New(dir string, opts ...Option) *Poller {
	p := &Poller{
		dir:        dir,
		executable: "gh",
		interval:   60 * time.Second,
		timeout:    3 * time.Second,
		logf:       log.Printf,
		done:       make(chan struct{}),
	}
	for _, opt := range opts {
		opt(p)
	}
	p.ctx, p.cancel = context.WithCancel(context.Background())
	go p.loop()
	return p
}

// PR returns the most recently fetched pull request, or nil when there is
// none (no PR, gh unavailable, or the first lookup has not completed yet).
// It never blocks on gh and never returns an error.
func (p *Poller) PR() *PR {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pr == nil {
		return nil
	}
	pr := *p.pr
	return &pr
}

// Stop halts the refresh loop and waits for it to finish.
func (p *Poller) Stop() {
	p.once.Do(p.cancel)
	<-p.done
}

func (p *Poller) loop() {
	defer close(p.done)
	p.refresh()
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			p.refresh()
		}
	}
}

func (p *Poller) refresh() {
	pr, err := p.fetch()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pr = pr
	if err == nil {
		p.lastFail = ""
		return
	}
	if key := err.Error(); key != p.lastFail {
		p.lastFail = key
		p.logf("ghinfo: PR lookup unavailable: %v", err)
	}
}

func (p *Poller) fetch() (*PR, error) {
	ctx, cancel := context.WithTimeout(p.ctx, p.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.executable, "pr", "view", "--json", "number,title,url")
	cmd.Dir = p.dir
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	var pr PR
	if err := json.Unmarshal(out, &pr); err != nil {
		return nil, errors.New("unexpected gh output")
	}
	if pr.Number <= 0 {
		return nil, errors.New("unexpected gh output")
	}
	return &pr, nil
}
