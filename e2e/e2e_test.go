// Package e2e smoke-tests the real marquee binary in wrapper mode: it
// builds marquee and the testupstream app, runs marquee as a subprocess
// wrapping testupstream inside a fixture git repo, and asserts the proxy's
// externally observable contracts — injection, Content-Length, Host
// preservation, byte-identical passthrough, WebSocket upgrade, unbuffered
// SSE, status data, clean shutdown, and the starting page.
package e2e

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	fixtureBranch = "marquee-e2e-fixture"
	// The real binary mints a per-process switch token, so the injected
	// element carries a token attribute whose value varies per run. Assertions
	// check the script tag and the element boundary rather than a fixed
	// token-less snippet.
	barScriptTag  = `<script type="module" src="/__marquee/bar.js"></script>`
	barElementEnd = `</marquee-bar>`
	wsGUID        = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
)

var (
	marqueeBin  string
	upstreamBin string
	repoDir     string
	shared      *marqueeProc
)

func TestMain(m *testing.M) {
	os.Exit(testMain(m))
}

func testMain(m *testing.M) int {
	tmp, err := os.MkdirTemp("", "marquee-e2e-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: temp dir: %v\n", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	marqueeBin = filepath.Join(tmp, "marquee")
	upstreamBin = filepath.Join(tmp, "testupstream")
	repoDir = filepath.Join(tmp, "repo")
	for target, pkg := range map[string]string{
		marqueeBin:  "github.com/jellehuibregtse/marquee/cmd/marquee",
		upstreamBin: "github.com/jellehuibregtse/marquee/e2e/testupstream",
	} {
		if err := goBuild(target, pkg); err != nil {
			fmt.Fprintf(os.Stderr, "e2e: %v\n", err)
			return 1
		}
	}
	if err := makeFixtureRepo(repoDir); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: fixture repo: %v\n", err)
		return 1
	}

	shared, err = startMarquee()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: %v\n", err)
		return 1
	}
	if err := shared.waitHealthy(15 * time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: %v\n", err)
		_ = shared.stop()
		return 1
	}

	code := m.Run()
	if err := shared.stop(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: stopping shared marquee: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

func goBuild(target, pkg string) error {
	cmd := exec.Command("go", "build", "-o", target, pkg)
	cmd.Dir = repoRoot()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go build %s: %v: %s", pkg, err, out)
	}
	return nil
}

func repoRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return filepath.Dir(wd)
}

// makeFixtureRepo creates a tiny git repo with a synthetic branch name so
// marquee's gitinfo poller has real data to report through /__marquee/status.
func makeFixtureRepo(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "app.txt"), []byte("fixture\n"), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", fixtureBranch},
		{"config", "user.email", "e2e@example.com"},
		{"config", "user.name", "e2e"},
		{"add", "."},
		{"commit", "-q", "-m", "fixture"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	return nil
}

type marqueeProc struct {
	cmd          *exec.Cmd
	addr         string
	baseURL      string
	internalPort int
}

// startMarquee launches the real marquee binary wrapping testupstream,
// with cwd inside the fixture repo. upstreamArgs are passed to the child.
func startMarquee(upstreamArgs ...string) (*marqueeProc, error) {
	listenPort, err := freePort()
	if err != nil {
		return nil, err
	}
	internalPort, err := freePort()
	if err != nil {
		return nil, err
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(listenPort))
	args := append([]string{
		"--listen", addr,
		"--internal-port", strconv.Itoa(internalPort),
		"--", upstreamBin,
	}, upstreamArgs...)
	cmd := exec.Command(marqueeBin, args...)
	cmd.Dir = repoDir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting marquee: %w", err)
	}
	return &marqueeProc{cmd: cmd, addr: addr, baseURL: "http://" + addr, internalPort: internalPort}, nil
}

func (p *marqueeProc) waitHealthy(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(p.baseURL + "/")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("marquee at %s not healthy within %s", p.addr, timeout)
}

func (p *marqueeProc) stop() error {
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	return p.wait(10 * time.Second)
}

func (p *marqueeProc) wait(timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		_ = p.cmd.Process.Kill()
		return fmt.Errorf("marquee did not exit within %s", timeout)
	}
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	return port, ln.Close()
}

func get(t *testing.T, url string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s body: %v", url, err)
	}
	return resp, body
}

func TestBarInjectedBeforeFinalBodyClose(t *testing.T) {
	resp, body := get(t, shared.baseURL+"/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	idx := bytes.LastIndex(body, []byte("</body>"))
	if idx < 0 {
		t.Fatalf("no </body> in response:\n%s", body)
	}
	if !bytes.HasSuffix(body[:idx], []byte(barElementEnd)) || !bytes.Contains(body[:idx], []byte(barScriptTag)) {
		t.Fatalf("bar snippet not immediately before final </body>:\n%s", body)
	}
	if cl := resp.Header.Get("Content-Length"); cl != strconv.Itoa(len(body)) {
		t.Fatalf("Content-Length = %q, want %d (actual bytes received)", cl, len(body))
	}
}

func TestHostHeaderPreserved(t *testing.T) {
	_, port, err := net.SplitHostPort(shared.addr)
	if err != nil {
		t.Fatal(err)
	}
	host := "app.lvh.me:" + port
	req, err := http.NewRequest(http.MethodGet, shared.baseURL+"/echo-host", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != host {
		t.Fatalf("upstream saw Host %q, want %q", body, host)
	}
}

func TestJSONPassesThroughByteIdentical(t *testing.T) {
	directURL := fmt.Sprintf("http://127.0.0.1:%d/data.json", shared.internalPort)
	_, direct := get(t, directURL)
	resp, proxied := get(t, shared.baseURL+"/data.json")
	if !bytes.Equal(direct, proxied) {
		t.Fatalf("proxied JSON differs from upstream:\nupstream: %s\nproxied:  %s", direct, proxied)
	}
	if bytes.Contains(proxied, []byte("<marquee-bar")) {
		t.Fatalf("bar snippet leaked into JSON: %s", proxied)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
}

func TestWebSocketEchoThroughProxy(t *testing.T) {
	conn, err := net.DialTimeout("tcp", shared.addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}

	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(nonce)
	handshake := "GET /ws HTTP/1.1\r\n" +
		"Host: " + shared.addr + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err := io.WriteString(conn, handshake); err != nil {
		t.Fatal(err)
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("handshake status = %d, want 101", resp.StatusCode)
	}
	sum := sha1.Sum([]byte(key + wsGUID))
	if got, want := resp.Header.Get("Sec-WebSocket-Accept"), base64.StdEncoding.EncodeToString(sum[:]); got != want {
		t.Fatalf("Sec-WebSocket-Accept = %q, want %q", got, want)
	}

	payload := []byte("ping")
	mask := [4]byte{0x12, 0x34, 0x56, 0x78}
	frame := []byte{0x81, 0x80 | byte(len(payload))}
	frame = append(frame, mask[:]...)
	for i, b := range payload {
		frame = append(frame, b^mask[i%4])
	}
	if _, err := conn.Write(frame); err != nil {
		t.Fatal(err)
	}

	var header [2]byte
	if _, err := io.ReadFull(br, header[:]); err != nil {
		t.Fatal(err)
	}
	if header[0] != 0x81 || header[1] != byte(len(payload)) {
		t.Fatalf("echo frame header = %#v, want text frame of %d unmasked bytes", header, len(payload))
	}
	echo := make([]byte, len(payload))
	if _, err := io.ReadFull(br, echo); err != nil {
		t.Fatal(err)
	}
	if string(echo) != string(payload) {
		t.Fatalf("echo payload = %q, want %q", echo, payload)
	}
}

func TestSSEStreamsWithoutBufferingDelay(t *testing.T) {
	start := time.Now()
	resp, err := http.Get(shared.baseURL + "/sse")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	type read struct {
		line string
		err  error
	}
	lines := make(chan read, 1)
	go func() {
		line, err := bufio.NewReader(resp.Body).ReadString('\n')
		lines <- read{line, err}
	}()
	select {
	case r := <-lines:
		if r.err != nil {
			t.Fatalf("reading first event: %v", r.err)
		}
		if r.line != "data: first\n" {
			t.Fatalf("first line = %q, want %q", r.line, "data: first\n")
		}
		// The upstream sends the second event only after 2s; the first
		// arriving well before that proves the proxy is not buffering.
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("first SSE event took %s; proxy appears to buffer the stream", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the first SSE event")
	}
}

func TestStatusReportsFixtureBranch(t *testing.T) {
	resp, body := get(t, shared.baseURL+"/__marquee/status")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, body)
	}
	var payload struct {
		Branch string `json:"branch"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decoding status JSON: %v: %s", err, body)
	}
	if payload.Branch != fixtureBranch {
		t.Fatalf("branch = %q, want %q", payload.Branch, fixtureBranch)
	}
}

func TestSIGTERMStopsChildWithoutOrphans(t *testing.T) {
	proc, err := startMarquee("-tag=shutdown")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.stop() })
	if err := proc.waitHealthy(15 * time.Second); err != nil {
		t.Fatal(err)
	}

	if err := proc.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := proc.wait(10 * time.Second); err != nil {
		t.Fatalf("marquee did not exit cleanly after SIGTERM: %v", err)
	}

	upstreamAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(proc.internalPort))
	waitFor(t, 5*time.Second, "upstream port to stop accepting", func() bool {
		conn, err := net.DialTimeout("tcp", upstreamAddr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return false
		}
		return true
	})
	waitFor(t, 5*time.Second, "child process to disappear", func() bool {
		return !processRunning(t, upstreamBin+" -tag=shutdown")
	})
}

// processRunning reports whether pgrep -f finds a process whose command
// line matches pattern. The pattern includes the per-run temp dir path, so
// unrelated processes can never match.
func processRunning(t *testing.T, pattern string) bool {
	t.Helper()
	err := exec.Command("pgrep", "-f", pattern).Run()
	if err == nil {
		return true
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false
	}
	t.Fatalf("pgrep -f %q: %v", pattern, err)
	return false
}

func TestStartingPageWhileChildBoots(t *testing.T) {
	proc, err := startMarquee("-delay=2s")
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
		t.Fatalf("status while child boots = %d, want 503", resp.StatusCode)
	}
	if !bytes.Contains(body, []byte(`http-equiv="refresh"`)) {
		t.Fatalf("starting page is not self-refreshing:\n%s", body)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, body = getHTML(t, proc.baseURL+"/")
		if resp.StatusCode == http.StatusOK {
			if !bytes.Contains(body, []byte(barScriptTag)) || !bytes.Contains(body, []byte(barElementEnd)) {
				t.Fatalf("bar snippet missing once upstream is up:\n%s", body)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("still %d after deadline, upstream never became reachable", resp.StatusCode)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func getHTML(t *testing.T, url string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/html")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s body: %v", url, err)
	}
	return resp, body
}

func waitFor(t *testing.T, timeout time.Duration, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}
