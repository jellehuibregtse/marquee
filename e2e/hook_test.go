package e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The launch worktree is bootstrapped like any other: the switch hook runs there
// before the first child start. The child is gated on the file the hook creates,
// so it can only come up if the hook ran first — a marquee that starts the child
// before (or without) the hook leaves the app dead.
func TestStartupHookRunsBeforeTheInitialChildStart(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	if err := makeFixtureRepo(repo); err != nil {
		t.Fatalf("build repo: %v", err)
	}

	proc, err := startMarqueeWith(repo,
		[]string{"--switch-hook", "touch bootstrapped"},
		[]string{"sh", "-c", "test -f bootstrapped && exec " + upstreamBin},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.stop() })

	if err := proc.waitHealthy(15 * time.Second); err != nil {
		t.Fatalf("the app never came up, so the child did not see the hook's work: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "bootstrapped")); err != nil {
		t.Fatalf("hook did not run in the launch worktree %q: %v", repo, err)
	}
}

// A failing hook at startup refuses the boot: marquee exits non-zero, the child
// is never started, and no pidfile is left behind claiming a child that does not
// exist. This mirrors a switch whose hook fails before anything moved.
func TestFailingStartupHookRefusesToBoot(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	if err := makeFixtureRepo(repo); err != nil {
		t.Fatalf("build repo: %v", err)
	}

	proc, err := startMarqueeWith(repo,
		[]string{"--switch-hook", "echo bootstrap is broken >&2; exit 7"},
		[]string{upstreamBin, "-tag=hook-refused"},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.cmd.Process.Kill() })

	if err := proc.wait(15 * time.Second); err == nil {
		t.Fatal("marquee exited 0 after its startup hook failed, want a non-zero exit")
	}
	logged := proc.output.String()
	if !strings.Contains(logged, "bootstrap is broken") {
		t.Errorf("marquee did not stream the hook's own output:\n%s", logged)
	}
	if !strings.Contains(logged, "refusing to start the child") {
		t.Errorf("marquee did not say why it refused to boot:\n%s", logged)
	}

	if processRunning(t, upstreamBin+" -tag=hook-refused") {
		t.Error("the child was started even though the startup hook failed")
	}
	upstreamAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(proc.internalPort))
	if conn, err := net.DialTimeout("tcp", upstreamAddr, 250*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Error("something is listening on the internal port, so a child did run")
	}
	if path := pidfileFor(t, proc.addr); path != "" {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("pidfile %s left behind by a boot that never started a child", path)
		}
	}
}

// A bootstrap can take minutes, and a browser that arrives during it must get
// the proxy's self-refreshing "app is starting" page rather than an accepted
// connection that is never answered.
func TestListenerServesTheStartingPageDuringTheStartupHook(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	if err := makeFixtureRepo(repo); err != nil {
		t.Fatalf("build repo: %v", err)
	}

	proc, err := startMarqueeWith(repo, []string{"--switch-hook", "sleep 5"}, []string{upstreamBin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.stop() })

	waitFor(t, 5*time.Second, "marquee listener to accept", func() bool {
		conn, err := net.DialTimeout("tcp", proc.addr, 100*time.Millisecond)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	})

	resp, body := getHTML(t, proc.baseURL+"/")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status during the bootstrap = %d, want 503", resp.StatusCode)
	}
	if !bytes.Contains(body, []byte(`http-equiv="refresh"`)) {
		t.Fatalf("the page served during the bootstrap is not the starting page:\n%s", body)
	}

	if err := proc.waitHealthy(20 * time.Second); err != nil {
		t.Fatalf("the app never came up after the bootstrap: %v", err)
	}
}

// Ctrl-C during a long bootstrap takes the hook's whole process group with it.
// The hook leads its own group, so without cancelling it on the signal marquee
// would exit and leave a "bundle install" running behind it.
func TestSignalDuringTheStartupHookStopsTheHook(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	if err := makeFixtureRepo(repo); err != nil {
		t.Fatalf("build repo: %v", err)
	}
	marker := filepath.Join(t.TempDir(), "hook-finished")

	proc, err := startMarqueeWith(repo,
		[]string{"--switch-hook", "sleep 30 && touch " + marker},
		[]string{upstreamBin},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.cmd.Process.Kill() })

	waitFor(t, 5*time.Second, "the hook to start", func() bool {
		return processRunning(t, marker)
	})
	if err := proc.cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	if err := proc.wait(15 * time.Second); err == nil {
		t.Error("marquee exited 0 after being interrupted mid-bootstrap")
	}

	waitFor(t, 10*time.Second, "the hook's process group to die", func() bool {
		return !processRunning(t, marker)
	})
	if _, err := os.Stat(marker); err == nil {
		t.Error("the hook ran to completion after marquee was interrupted")
	}
}

// --quiet hides marquee's progress lines, never the reason a boot was refused.
func TestQuietStillShowsWhyTheStartupHookFailed(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	if err := makeFixtureRepo(repo); err != nil {
		t.Fatalf("build repo: %v", err)
	}

	proc, err := startMarqueeWith(repo,
		[]string{"--quiet", "--switch-hook", "echo REASON-VISIBLE >&2; exit 7"},
		[]string{upstreamBin},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.cmd.Process.Kill() })

	if err := proc.wait(15 * time.Second); err == nil {
		t.Fatal("marquee exited 0 after its startup hook failed, want a non-zero exit")
	}
	if logged := proc.output.String(); !strings.Contains(logged, "REASON-VISIBLE") {
		t.Errorf("--quiet swallowed the reason the boot was refused:\n%s", logged)
	}
}

// pidfileFor mirrors cmd/marquee's pidfile naming (sha256 of the listen address,
// first eight bytes, under the user cache dir) so the test can assert the file is
// absent. An unavailable cache dir yields an empty path and the check is skipped.
func pidfileFor(t *testing.T, listen string) string {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(listen))
	return filepath.Join(cache, "marquee", hex.EncodeToString(sum[:8])+".pid")
}
