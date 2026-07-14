package status

import (
	"encoding/json"
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
}

var barScript, _ = bar.Assets.ReadFile("bar.js")

// Register wires GET /__marquee/status and GET /__marquee/bar.js onto
// the guarded mux, so both endpoints inherit the Host allowlist and
// Cache-Control: no-store guards by construction. The GET method
// patterns make the mux answer other methods with 405.
func Register(mux *proxy.InternalMux, deps Deps) {
	mux.Handle("GET /__marquee/status", statusHandler(deps))
	mux.Handle("GET /__marquee/bar.js", http.HandlerFunc(serveBarScript))
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

func serveBarScript(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	_, _ = w.Write(barScript)
}
