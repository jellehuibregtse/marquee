package status

import (
	"encoding/json"
	"io/fs"
	"net/http"

	"github.com/jellehuibregtse/marquee/internal/bar"
	"github.com/jellehuibregtse/marquee/internal/ghinfo"
	"github.com/jellehuibregtse/marquee/internal/gitinfo"
	"github.com/jellehuibregtse/marquee/internal/proxy"
)

// Deps supplies the data sources for the status payload as plain
// accessors, so this package stays decoupled from the pollers' and
// runner's lifecycles. Git is required; a nil PR reports no pull
// request and a nil ChildState reports an empty state.
type Deps struct {
	Git        func() gitinfo.Snapshot
	PR         func() *ghinfo.PR
	ChildState func() string
	// Position is where the bar renders ("top" or "bottom"); the bar
	// script reads it from the status payload and positions itself.
	Position string
	// Size is the bar's size preset ("small", "medium", or "large"); the
	// bar script reads it from the status payload and scales itself.
	Size string
}

type child struct {
	State string `json:"state"`
}

type payload struct {
	Branch    string                  `json:"branch"`
	Dirty     bool                    `json:"dirty"`
	Worktree  gitinfo.CurrentWorktree `json:"worktree"`
	RepoRoot  string                  `json:"repoRoot"`
	PR        *ghinfo.PR              `json:"pr"`
	Worktrees []gitinfo.Worktree      `json:"worktrees"`
	Child     child                   `json:"child"`
	Position  string                  `json:"position"`
	Size      string                  `json:"size"`
}

// Register wires GET /__marquee/status and a GET route per embedded bar module
// (bar.js, prefs.js, settings.js, …) onto the guarded mux, so every endpoint
// inherits the Host allowlist and Cache-Control: no-store guards by
// construction. The GET method patterns make the mux answer other methods with
// 405. Serving assets by their embedded file name means adding a module is just
// dropping a *.js file into internal/bar, no route wiring here.
func Register(mux *proxy.InternalMux, deps Deps) {
	mux.Handle("GET /__marquee/status", statusHandler(deps))
	registerAssets(mux)
}

func registerAssets(mux *proxy.InternalMux) {
	names, _ := fs.Glob(bar.Assets, "*.js")
	for _, name := range names {
		data, err := bar.Assets.ReadFile(name)
		if err != nil {
			continue
		}
		mux.Handle("GET /__marquee/"+name, serveJS(data))
	}
}

func serveJS(data []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = w.Write(data)
	})
}

func statusHandler(deps Deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		snap := deps.Git()
		p := payload{
			Branch:    snap.Branch,
			Dirty:     snap.Dirty,
			Worktree:  snap.Worktree,
			RepoRoot:  snap.RepoRoot,
			Worktrees: snap.Worktrees,
			Position:  deps.Position,
			Size:      deps.Size,
		}
		if p.Worktrees == nil {
			p.Worktrees = []gitinfo.Worktree{}
		}
		if deps.PR != nil {
			p.PR = deps.PR()
		}
		if deps.ChildState != nil {
			p.Child.State = deps.ChildState()
		}
		body, err := json.Marshal(p)
		if err != nil {
			http.Error(w, "marquee: encoding status", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
}
