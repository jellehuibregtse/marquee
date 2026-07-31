package e2e

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The real binary reads .marquee/config out of the directory it was launched in
// and applies it with no flag to say so. --allow-host is the check, because it is
// the one flag whose effect is visible from outside: the internal endpoints answer
// a host the file allowed and still refuse one it did not.
func TestConfigFileFlagsApplyWithoutBeingPassed(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	if err := makeFixtureRepo(repo); err != nil {
		t.Fatalf("build repo: %v", err)
	}
	if err := os.Mkdir(filepath.Join(repo, ".marquee"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := "# what this repo needs\n--allow-host '*.example.test'\n"
	if err := os.WriteFile(filepath.Join(repo, ".marquee", "config"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	proc, err := startMarqueeWith(repo, nil, []string{upstreamBin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.stop() })
	if err := proc.waitHealthy(15 * time.Second); err != nil {
		t.Fatal(err)
	}

	_, port, err := net.SplitHostPort(proc.addr)
	if err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]int{
		"app.example.test": http.StatusOK,
		"evil.test":        http.StatusForbidden,
	} {
		req, err := http.NewRequest(http.MethodGet, proc.baseURL+"/__marquee/status", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = net.JoinHostPort(host, port)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("Host %q: status = %d, want %d", host, resp.StatusCode, want)
		}
	}
	if logged := proc.output.String(); !strings.Contains(logged, filepath.Join(".marquee", "config")) {
		t.Errorf("marquee did not say it took flags from the file:\n%s", logged)
	}
}

// Abuse: the file cannot choose the process marquee spawns. A config that tries to
// smuggle a command in refuses the whole run instead of starting something nobody
// asked for.
func TestConfigFileCannotSetTheCommand(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	if err := makeFixtureRepo(repo); err != nil {
		t.Fatalf("build repo: %v", err)
	}
	if err := os.Mkdir(filepath.Join(repo, ".marquee"), 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(repo, "smuggled")
	config := "-- touch " + marker + "\n"
	if err := os.WriteFile(filepath.Join(repo, ".marquee", "config"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	proc, err := startMarqueeWith(repo, nil, []string{upstreamBin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.cmd.Process.Kill() })

	if err := proc.wait(15 * time.Second); err == nil {
		t.Fatal("marquee started with a command smuggled through its config file")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the config file's command ran")
	}
	if logged := proc.output.String(); !strings.Contains(logged, "not allowed") {
		t.Errorf("marquee did not say why it refused the file:\n%s", logged)
	}
	upstreamAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(proc.internalPort))
	if conn, err := net.DialTimeout("tcp", upstreamAddr, 250*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Error("something is listening on the internal port, so a child did run")
	}
}
