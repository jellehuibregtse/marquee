package e2e

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// tokenAttr pulls the per-process switch token out of the injected bar element,
// exactly as bar.js would read it before echoing it on the switch request.
var tokenAttr = regexp.MustCompile(`<marquee-bar token="([0-9a-f]+)"`)

// TestSwitchHappyPathOverHTTP drives POST /__marquee/switch against the real
// binary: it builds a repo with a second worktree, wraps testupstream in the
// main worktree, then switches into the second worktree over HTTP with the
// minted token and asserts a 200 plus the status endpoint reporting the new
// worktree's branch — the switch's externally observable success contract,
// which had no e2e coverage before.
func TestSwitchHappyPathOverHTTP(t *testing.T) {
	tmp := t.TempDir()
	mainWt := filepath.Join(tmp, "main")
	featureWt := filepath.Join(tmp, "feature")
	const featureBranch = "marquee-e2e-feature"
	if err := makeSwitchRepo(mainWt, featureWt, featureBranch); err != nil {
		t.Fatalf("build repo: %v", err)
	}

	proc, err := startMarqueeAt(mainWt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.stop() })
	if err := proc.waitHealthy(15 * time.Second); err != nil {
		t.Fatal(err)
	}

	token := switchToken(t, proc.baseURL)

	req, err := http.NewRequest(http.MethodPost, proc.baseURL+"/__marquee/switch", strings.NewReader(`{"slug":"feature"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = proc.addr
	req.Header.Set("Content-Type", "application/json")
	// No browser sets Sec-Fetch-Site here, so the switch guard falls back to
	// requiring an Origin whose scheme+host matches Host — which the real bar's
	// same-origin fetch satisfies.
	req.Header.Set("Origin", proc.baseURL)
	req.Header.Set("X-Marquee-Token", token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("switch status = %d, want 200; body %s", resp.StatusCode, body)
	}
	var switched struct {
		OK     bool   `json:"ok"`
		Slug   string `json:"slug"`
		IsMain bool   `json:"isMain"`
	}
	if err := json.Unmarshal(body, &switched); err != nil {
		t.Fatalf("switch response not JSON: %v: %s", err, body)
	}
	if !switched.OK || switched.Slug != "feature" || switched.IsMain {
		t.Fatalf("switch response = %+v, want ok slug=feature isMain=false", switched)
	}

	// The bar now reports the worktree the child actually restarted into: the
	// status poller was repointed to the feature worktree, whose branch differs.
	waitFor(t, 5*time.Second, "status to report the switched-into branch", func() bool {
		resp, body := get(t, proc.baseURL+"/__marquee/status")
		if resp.StatusCode != http.StatusOK {
			return false
		}
		var payload struct {
			Branch string `json:"branch"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return false
		}
		return payload.Branch == featureBranch
	})
}

// TestSwitchRejectsAGlobbedOutWorktree drives the real binary with a
// --worktree-glob that matches nothing, so only the main worktree survives, then
// asks it to switch into the excluded one. main has to hand the filter to the
// orchestrator as well as to the status payload: with only the payload filtered
// the picker would look restricted while the endpoint still spawned a process in
// any worktree git knows about, which is exactly what docs/security.md Threat 4
// says it will not do. The excluded slug must come back 400 unknown_slug with no
// restart.
func TestSwitchRejectsAGlobbedOutWorktree(t *testing.T) {
	tmp := t.TempDir()
	mainWt := filepath.Join(tmp, "main")
	featureWt := filepath.Join(tmp, "feature")
	if err := makeSwitchRepo(mainWt, featureWt, "marquee-e2e-globbed-out"); err != nil {
		t.Fatalf("build repo: %v", err)
	}

	globs := []string{"--worktree-glob", filepath.Join(tmp, "matches-nothing", "*")}
	proc, err := startMarqueeWith(mainWt, globs, []string{upstreamBin, "-tag=globbed-out"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.stop() })
	if err := proc.waitHealthy(15 * time.Second); err != nil {
		t.Fatal(err)
	}

	// The picker side: the excluded worktree is not even offered.
	_, body := get(t, proc.baseURL+"/__marquee/status")
	var payload struct {
		Worktrees []struct {
			Slug string `json:"slug"`
		} `json:"worktrees"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("status not JSON: %v: %s", err, body)
	}
	for _, wt := range payload.Worktrees {
		if wt.Slug == "feature" {
			t.Errorf("status still offers the globbed-out worktree: %s", body)
		}
	}

	pattern := upstreamBin + " -tag=globbed-out"
	before := processIDs(t, pattern)

	status, switchBody := postSwitch(t, proc, `{"slug":"feature","confirm":true}`)
	if status != http.StatusBadRequest {
		t.Fatalf("switch status = %d, want 400; body %s", status, switchBody)
	}
	var rejected struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(switchBody, &rejected); err != nil {
		t.Fatalf("switch response not JSON: %v: %s", err, switchBody)
	}
	if rejected.Error != "unknown_slug" {
		t.Errorf("error = %q, want %q", rejected.Error, "unknown_slug")
	}

	// A restart would replace the child, so an unchanged process set is the
	// no-process-action proof. The set holds marquee itself too, since its own argv
	// carries the child command; only the child's pid could have changed.
	if after := processIDs(t, pattern); after != before {
		t.Errorf("process set changed from %q to %q: the rejected switch still restarted the child", before, after)
	}
}

// TestSwitchFailsWhenReadyCmdNeverPasses covers the rest of main's switch wiring:
// a --ready-cmd that always fails must fail the switch even though the child's
// port comes up fine, and it must give up on the --health-timeout it was handed.
// A --health-timeout that never reached the orchestrator would fall back to 30s
// and blow the bound below.
func TestSwitchFailsWhenReadyCmdNeverPasses(t *testing.T) {
	tmp := t.TempDir()
	mainWt := filepath.Join(tmp, "main")
	featureWt := filepath.Join(tmp, "feature")
	if err := makeSwitchRepo(mainWt, featureWt, "marquee-e2e-not-ready"); err != nil {
		t.Fatalf("build repo: %v", err)
	}

	args := []string{"--ready-cmd", "exit 1", "--health-timeout", "1s"}
	proc, err := startMarqueeWith(mainWt, args, []string{upstreamBin, "-tag=not-ready"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.stop() })
	if err := proc.waitHealthy(15 * time.Second); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	status, body := postSwitch(t, proc, `{"slug":"feature","confirm":true}`)
	elapsed := time.Since(start)
	if status != http.StatusBadGateway {
		t.Fatalf("switch status = %d, want 502; body %s", status, body)
	}
	var failed struct {
		Error    string `json:"error"`
		Reverted bool   `json:"reverted"`
	}
	if err := json.Unmarshal(body, &failed); err != nil {
		t.Fatalf("switch response not JSON: %v: %s", err, body)
	}
	if failed.Error != "switch_failed" || !failed.Reverted {
		t.Errorf("switch response = %+v, want switch_failed reverted=true", failed)
	}
	if elapsed > 15*time.Second {
		t.Errorf("switch took %s with --health-timeout 1s: the flag did not reach the switch", elapsed)
	}

	// The revert is not gated on --ready-cmd, so the app serves again.
	if err := proc.waitHealthy(15 * time.Second); err != nil {
		t.Fatalf("app did not come back after the reverted switch: %v", err)
	}
}

// postSwitch posts a switch request the way the injected bar does: a same-origin
// Origin header plus the minted token.
func postSwitch(t *testing.T, proc *marqueeProc, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, proc.baseURL+"/__marquee/switch", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = proc.addr
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", proc.baseURL)
	req.Header.Set("X-Marquee-Token", switchToken(t, proc.baseURL))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, out
}

// processIDs returns the pids pgrep -f finds for pattern as one comparable
// string. The pattern carries the per-run temp dir, so nothing unrelated matches.
func processIDs(t *testing.T, pattern string) string {
	t.Helper()
	out, err := exec.Command("pgrep", "-f", pattern).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return ""
		}
		t.Fatalf("pgrep -f %q: %v", pattern, err)
	}
	fields := strings.Fields(string(out))
	sort.Strings(fields)
	return strings.Join(fields, ",")
}

// switchToken fetches an injected page and extracts the minted switch token.
func switchToken(t *testing.T, baseURL string) string {
	t.Helper()
	_, body := get(t, baseURL+"/")
	m := tokenAttr.FindSubmatch(body)
	if m == nil {
		t.Fatalf("no switch token in injected bar element:\n%s", body)
	}
	return string(m[1])
}

// makeSwitchRepo builds a git repo at main with an initial commit, then adds a
// second worktree at feature on its own branch, so a switch has a real target.
func makeSwitchRepo(main, feature, featureBranch string) error {
	if err := os.MkdirAll(main, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(main, "app.txt"), []byte("main\n"), 0o644); err != nil {
		return err
	}
	if err := assertNoRepository(main); err != nil {
		return err
	}
	steps := [][]string{
		{"init", "-q", "-b", "marquee-e2e-main"},
		{"add", "."},
		{"commit", "-q", "-m", "fixture"},
		{"worktree", "add", "-q", "-b", featureBranch, feature},
	}
	for _, args := range steps {
		if err := runFixtureGit(main, args...); err != nil {
			return err
		}
	}
	return nil
}
