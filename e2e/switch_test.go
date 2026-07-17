package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	steps := [][]string{
		{"init", "-q", "-b", "marquee-e2e-main"},
		{"config", "user.email", "e2e@example.com"},
		{"config", "user.name", "e2e"},
		{"config", "commit.gpgsign", "false"},
		{"add", "."},
		{"commit", "-q", "-m", "fixture"},
		{"worktree", "add", "-q", "-b", featureBranch, feature},
	}
	for _, args := range steps {
		cmd := exec.Command("git", args...)
		cmd.Dir = main
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	return nil
}
