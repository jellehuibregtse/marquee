package gitinfo

import (
	"log"
	"sync"
	"time"
)

// Poller refreshes a Snapshot of the git state in dir on an interval and
// serves the latest good one from cache. On git failure it keeps serving the
// stale snapshot and logs once per distinct error, so a broken or missing git
// never breaks the status endpoint.
type Poller struct {
	dir      string
	interval time.Duration
	logf     func(format string, args ...any)

	mu      sync.Mutex
	snap    Snapshot
	lastErr string

	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
}

// Start collects a first snapshot synchronously (so status is never empty),
// then refreshes every interval until Stop. A non-positive interval defaults
// to 2s; a nil logf defaults to log.Printf. In a non-git dir the snapshot
// stays zero and the poller keeps running.
func Start(dir string, interval time.Duration, logf func(format string, args ...any)) *Poller {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	if logf == nil {
		logf = log.Printf
	}
	p := &Poller{
		dir:      dir,
		interval: interval,
		logf:     logf,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	p.refresh()
	go p.loop()
	return p
}

// Snapshot returns the latest cached snapshot.
func (p *Poller) Snapshot() Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.snap
}

// Repoint atomically swaps the directory the poller collects from and
// refreshes immediately, so a caller (the worktree switcher) can point the
// bar at a new worktree and have the very next status read reflect it. The
// swap and the concurrent Snapshot reads are both guarded by the same mutex,
// so a reader never observes a torn state.
func (p *Poller) Repoint(dir string) {
	p.mu.Lock()
	p.dir = dir
	p.mu.Unlock()
	p.refresh()
}

// Stop halts the poll loop and waits for it to finish. Safe to call twice.
func (p *Poller) Stop() {
	p.stopOnce.Do(func() { close(p.stop) })
	<-p.done
}

func (p *Poller) loop() {
	defer close(p.done)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-ticker.C:
			p.refresh()
		}
	}
}

func (p *Poller) refresh() {
	p.mu.Lock()
	dir := p.dir
	p.mu.Unlock()
	snap, err := collect(dir)
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		if err.Error() != p.lastErr {
			p.lastErr = err.Error()
			p.logf("gitinfo: %v; serving last known state", err)
		}
		return
	}
	p.lastErr = ""
	p.snap = snap
}
