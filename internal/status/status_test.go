package status_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jellehuibregtse/marquee/internal/bar"
	"github.com/jellehuibregtse/marquee/internal/ghinfo"
	"github.com/jellehuibregtse/marquee/internal/gitinfo"
	"github.com/jellehuibregtse/marquee/internal/proxy"
	"github.com/jellehuibregtse/marquee/internal/status"
)

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func fixtureRepo(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "init", "-b", "trunk")
	gitCmd(t, dir, "config", "user.name", "Fixture Author")
	gitCmd(t, dir, "config", "user.email", "fixture@example.com")
	gitCmd(t, dir, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", ".")
	gitCmd(t, dir, "commit", "-m", "Add notes")
	return dir
}

func fixtureDeps(t *testing.T, pr func() *ghinfo.PR) (status.Deps, string) {
	t.Helper()
	dir := fixtureRepo(t)
	poller := gitinfo.Start(dir, time.Hour, nil)
	t.Cleanup(poller.Stop)
	return status.Deps{
		Git:        poller.Snapshot,
		PR:         pr,
		ChildState: func() string { return "running" },
		Position:   "bottom",
		Size:       "medium",
		Theme:      "default",
	}, dir
}

func newMux(t *testing.T, deps status.Deps) *proxy.InternalMux {
	t.Helper()
	mux := proxy.NewInternalMux()
	status.Register(mux, deps)
	return mux
}

func get(mux http.Handler, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func keysOf(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal object: %v", err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func assertKeys(t *testing.T, what string, raw json.RawMessage, want ...string) map[string]json.RawMessage {
	t.Helper()
	sorted := append([]string(nil), want...)
	sort.Strings(sorted)
	got := keysOf(t, raw)
	if strings.Join(got, ",") != strings.Join(sorted, ",") {
		t.Errorf("%s keys = %v, want %v", what, got, sorted)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestStatusJSONShape(t *testing.T) {
	deps, dir := fixtureDeps(t, func() *ghinfo.PR { return nil })
	mux := newMux(t, deps)

	rec := get(mux, "http://localhost/__marquee/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}

	doc := assertKeys(t, "status", rec.Body.Bytes(),
		"branch", "dirty", "worktree", "repoRoot", "pr", "worktrees", "child", "position", "size", "theme", "pills")
	assertKeys(t, "worktree", doc["worktree"], "path", "slug", "isMain")
	assertKeys(t, "child", doc["child"], "state")

	if string(doc["position"]) != `"bottom"` {
		t.Errorf("position = %s, want \"bottom\"", doc["position"])
	}
	if string(doc["size"]) != `"medium"` {
		t.Errorf("size = %s, want \"medium\"", doc["size"])
	}
	if string(doc["theme"]) != `"default"` {
		t.Errorf("theme = %s, want \"default\"", doc["theme"])
	}

	if string(doc["branch"]) != `"trunk"` {
		t.Errorf("branch = %s, want \"trunk\"", doc["branch"])
	}
	if string(doc["dirty"]) != "false" {
		t.Errorf("dirty = %s, want false", doc["dirty"])
	}
	if string(doc["pr"]) != "null" {
		t.Errorf("pr = %s, want null", doc["pr"])
	}
	var repoRoot string
	if err := json.Unmarshal(doc["repoRoot"], &repoRoot); err != nil || repoRoot != dir {
		t.Errorf("repoRoot = %s, want %q", doc["repoRoot"], dir)
	}
	var childDoc struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(doc["child"], &childDoc); err != nil || childDoc.State != "running" {
		t.Errorf("child = %s, want state \"running\"", doc["child"])
	}
	var worktrees []json.RawMessage
	if err := json.Unmarshal(doc["worktrees"], &worktrees); err != nil {
		t.Fatalf("worktrees not an array: %s", doc["worktrees"])
	}
	if len(worktrees) != 1 {
		t.Fatalf("worktrees = %s, want one entry", doc["worktrees"])
	}
	assertKeys(t, "worktrees[0]", worktrees[0], "slug", "path", "branch")
}

func TestStatusIncludesPR(t *testing.T) {
	pr := &ghinfo.PR{Number: 12, Title: "Add lantern", URL: "https://example.com/pull/12"}
	deps, _ := fixtureDeps(t, func() *ghinfo.PR { return pr })
	mux := newMux(t, deps)

	rec := get(mux, "http://localhost/__marquee/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	doc := assertKeys(t, "status", rec.Body.Bytes(),
		"branch", "dirty", "worktree", "repoRoot", "pr", "worktrees", "child", "position", "size", "theme", "pills")
	assertKeys(t, "pr", doc["pr"], "number", "title", "url")
	var got ghinfo.PR
	if err := json.Unmarshal(doc["pr"], &got); err != nil || got != *pr {
		t.Errorf("pr = %s, want %+v", doc["pr"], *pr)
	}
}

func TestStatusReportsPosition(t *testing.T) {
	deps, _ := fixtureDeps(t, func() *ghinfo.PR { return nil })
	deps.Position = "top"
	mux := newMux(t, deps)

	rec := get(mux, "http://localhost/__marquee/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if string(doc["position"]) != `"top"` {
		t.Errorf("position = %s, want \"top\"", doc["position"])
	}
}

func TestStatusReportsSize(t *testing.T) {
	deps, _ := fixtureDeps(t, func() *ghinfo.PR { return nil })
	deps.Size = "large"
	mux := newMux(t, deps)

	rec := get(mux, "http://localhost/__marquee/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if string(doc["size"]) != `"large"` {
		t.Errorf("size = %s, want \"large\"", doc["size"])
	}
}

func TestStatusReportsTheme(t *testing.T) {
	deps, _ := fixtureDeps(t, func() *ghinfo.PR { return nil })
	deps.Theme = "midnight"
	mux := newMux(t, deps)

	rec := get(mux, "http://localhost/__marquee/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if string(doc["theme"]) != `"midnight"` {
		t.Errorf("theme = %s, want \"midnight\"", doc["theme"])
	}
}

func TestStatusReportsPillsInOrder(t *testing.T) {
	deps, _ := fixtureDeps(t, func() *ghinfo.PR { return nil })
	deps.Pills = []string{"branch", "pr"}
	mux := newMux(t, deps)

	rec := get(mux, "http://localhost/__marquee/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if string(doc["pills"]) != `["branch","pr"]` {
		t.Errorf("pills = %s, want [\"branch\",\"pr\"]", doc["pills"])
	}
}

func TestStatusReportsEmptyPillsAsArray(t *testing.T) {
	deps, _ := fixtureDeps(t, func() *ghinfo.PR { return nil })
	deps.Pills = []string{}
	mux := newMux(t, deps)

	rec := get(mux, "http://localhost/__marquee/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if string(doc["pills"]) != `[]` {
		t.Errorf("pills = %s, want []", doc["pills"])
	}
}

func TestStatusEmptySnapshotSerializesEmptyWorktreeList(t *testing.T) {
	mux := newMux(t, status.Deps{Git: func() gitinfo.Snapshot { return gitinfo.Snapshot{} }})

	rec := get(mux, "http://localhost/__marquee/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if string(doc["worktrees"]) != "[]" {
		t.Errorf("worktrees = %s, want []", doc["worktrees"])
	}
	if string(doc["pr"]) != "null" {
		t.Errorf("pr = %s, want null", doc["pr"])
	}
}

func TestStatusMethodNotAllowed(t *testing.T) {
	deps, _ := fixtureDeps(t, nil)
	mux := newMux(t, deps)

	for _, path := range []string{"/__marquee/status", "/__marquee/bar.js"} {
		req := httptest.NewRequest(http.MethodPost, "http://localhost"+path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d, want 405", path, rec.Code)
		}
	}
}

func TestHostGuardEnforcedThroughMux(t *testing.T) {
	deps, _ := fixtureDeps(t, nil)
	mux := newMux(t, deps)

	for _, path := range []string{"/__marquee/status", "/__marquee/bar.js"} {
		req := httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil)
		req.Host = "evil.com"
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("GET %s with Host evil.com = %d, want 403", path, rec.Code)
		}
	}
}

func TestBarScriptServed(t *testing.T) {
	deps, _ := fixtureDeps(t, nil)
	mux := newMux(t, deps)

	rec := get(mux, "http://localhost/__marquee/bar.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("Content-Type = %q, want text/javascript", ct)
	}
	want, err := bar.Assets.ReadFile("bar.js")
	if err != nil {
		t.Fatal(err)
	}
	if len(want) == 0 {
		t.Fatal("embedded bar.js is empty")
	}
	if rec.Body.String() != string(want) {
		t.Error("served bar.js does not match the embedded bytes")
	}
}

func TestStatusThroughProxyHandler(t *testing.T) {
	var mu sync.Mutex
	var upstreamPaths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		upstreamPaths = append(upstreamPaths, r.URL.Path)
		mu.Unlock()
		_, _ = io.WriteString(w, "upstream")
	}))
	defer upstream.Close()
	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(upstreamURL.Port())
	if err != nil {
		t.Fatal(err)
	}

	deps, _ := fixtureDeps(t, nil)
	handler := proxy.New(proxy.Config{InternalPort: port})
	status.Register(handler.Internal(), deps)
	front := httptest.NewServer(handler)
	defer front.Close()

	resp, err := http.Get(front.URL + "/__marquee/status")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("status body is not JSON: %s", body)
	}
	if string(doc["branch"]) != `"trunk"` {
		t.Errorf("branch = %s, want \"trunk\"", doc["branch"])
	}

	appResp, err := http.Get(front.URL + "/hello")
	if err != nil {
		t.Fatal(err)
	}
	_ = appResp.Body.Close()

	mu.Lock()
	defer mu.Unlock()
	for _, path := range upstreamPaths {
		if strings.HasPrefix(path, "/__marquee") {
			t.Errorf("upstream saw internal path %s", path)
		}
	}
	if len(upstreamPaths) != 1 || upstreamPaths[0] != "/hello" {
		t.Errorf("upstream paths = %v, want [/hello]", upstreamPaths)
	}
}
