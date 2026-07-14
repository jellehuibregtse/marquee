package proxy

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newHandler(t *testing.T, port int, cfg Config) *Handler {
	t.Helper()
	cfg.InternalPort = port
	if cfg.Logger == nil {
		cfg.Logger = log.New(io.Discard, "", 0)
	}
	if cfg.ProbeTimeout == 0 {
		cfg.ProbeTimeout = 100 * time.Millisecond
	}
	if cfg.ProbeTTL == 0 {
		cfg.ProbeTTL = time.Millisecond
	}
	return New(cfg)
}

func upstreamPort(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func TestHostPreservedVerbatim(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Host)
	}))
	defer upstream.Close()

	proxySrv := httptest.NewServer(newHandler(t, upstreamPort(t, upstream), Config{}))
	defer proxySrv.Close()

	req, err := http.NewRequest(http.MethodGet, proxySrv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "app.lvh.me:3000"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); got != "app.lvh.me:3000" {
		t.Fatalf("upstream saw Host %q, want %q", got, "app.lvh.me:3000")
	}
}

func TestAcceptEncodingForcedToIdentity(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Header.Get("Accept-Encoding"))
	}))
	defer upstream.Close()

	proxySrv := httptest.NewServer(newHandler(t, upstreamPort(t, upstream), Config{}))
	defer proxySrv.Close()

	req, err := http.NewRequest(http.MethodGet, proxySrv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", "gzip, br")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); got != "identity" {
		t.Fatalf("upstream saw Accept-Encoding %q, want %q", got, "identity")
	}
}

func TestBodyPassedThroughByteIdentical(t *testing.T) {
	payload := make([]byte, 256<<10)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(payload)
	}))
	defer upstream.Close()

	proxySrv := httptest.NewServer(newHandler(t, upstreamPort(t, upstream), Config{}))
	defer proxySrv.Close()

	resp, err := http.Get(proxySrv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, payload) {
		t.Fatalf("body altered in transit: got %d bytes, want %d identical bytes", len(body), len(payload))
	}
}

func TestChunkedResponseStreamsWithoutBuffering(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "data: second\n\n")
	}))
	defer upstream.Close()

	proxySrv := httptest.NewServer(newHandler(t, upstreamPort(t, upstream), Config{}))
	defer proxySrv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(proxySrv.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	reader := bufio.NewReader(resp.Body)
	first, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("reading first chunk (proxy buffered the stream?): %v", err)
	}
	if first != "data: first\n" {
		t.Fatalf("first chunk = %q, want %q", first, "data: first\n")
	}
	close(release)
	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rest), "data: second") {
		t.Fatalf("rest of stream = %q, want it to contain %q", rest, "data: second")
	}
}

func TestUpgradePassthrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "echo" {
			http.Error(w, "expected upgrade", http.StatusBadRequest)
			return
		}
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
		if err := buf.Flush(); err != nil {
			return
		}
		_, _ = io.Copy(conn, buf.Reader)
	}))
	defer upstream.Close()

	proxySrv := httptest.NewServer(newHandler(t, upstreamPort(t, upstream), Config{}))
	defer proxySrv.Close()

	proxyURL, err := url.Parse(proxySrv.URL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", proxyURL.Host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	_, err = fmt.Fprintf(conn, "GET /echo HTTP/1.1\r\nHost: localhost\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	status, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "101") {
		t.Fatalf("status line = %q, want 101 Switching Protocols", status)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	if _, err := io.WriteString(conn, "ping\n"); err != nil {
		t.Fatal(err)
	}
	echoed, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if echoed != "ping\n" {
		t.Fatalf("echoed = %q, want %q", echoed, "ping\n")
	}
}

func TestInternalNamespaceNeverProxied(t *testing.T) {
	var upstreamHits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
	}))
	defer upstream.Close()

	proxySrv := httptest.NewServer(newHandler(t, upstreamPort(t, upstream), Config{}))
	defer proxySrv.Close()

	for _, path := range []string{"/__marquee", "/__marquee/", "/__marquee/status", "/__marquee/deep/path"} {
		req, err := http.NewRequest(http.MethodGet, proxySrv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "localhost"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404 (nothing registered yet)", path, resp.StatusCode)
		}
	}
	if n := upstreamHits.Load(); n != 0 {
		t.Fatalf("upstream received %d requests for /__marquee paths, want 0", n)
	}
}

func TestInternalHostGuard(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	proxySrv := httptest.NewServer(newHandler(t, upstreamPort(t, upstream), Config{AllowHosts: []string{"myapp.test"}}))
	defer proxySrv.Close()

	tests := []struct {
		host string
		want int
	}{
		{"evil.com", http.StatusForbidden},
		{"evil.com:3000", http.StatusForbidden},
		{"localhost.evil.com", http.StatusForbidden},
		{"lvh.me.evil.com", http.StatusForbidden},
		{"localhost", http.StatusNotFound},
		{"localhost:3000", http.StatusNotFound},
		{"127.0.0.1:3000", http.StatusNotFound},
		{"[::1]:3000", http.StatusNotFound},
		{"app.localhost", http.StatusNotFound},
		{"app.lvh.me:3000", http.StatusNotFound},
		{"MyApp.Test:8080", http.StatusNotFound},
	}
	for _, tc := range tests {
		req, err := http.NewRequest(http.MethodGet, proxySrv.URL+"/__marquee/status", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = tc.host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("Host %q: status = %d, want %d", tc.host, resp.StatusCode, tc.want)
		}
		if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("Host %q: Cache-Control = %q, want %q", tc.host, cc, "no-store")
		}
	}

	if NewInternalMux().hostAllowed("") {
		t.Error("empty Host allowed, want rejected")
	}
}

func TestInternalMuxRegistrationGoesThroughGuard(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	h := newHandler(t, upstreamPort(t, upstream), Config{})
	h.Internal().HandleFunc("/__marquee/ping", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "pong")
	})
	proxySrv := httptest.NewServer(h)
	defer proxySrv.Close()

	req, err := http.NewRequest(http.MethodGet, proxySrv.URL+"/__marquee/ping", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "evil.com"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("registered handler reachable with forbidden Host: status = %d, want 403", resp.StatusCode)
	}

	req.Host = "localhost"
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || string(body) != "pong" {
		t.Fatalf("allowed Host: status = %d body = %q, want 200 %q", resp.StatusCode, body, "pong")
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want %q", cc, "no-store")
	}
}

func TestStartingPageWhileUpstreamNotReady(t *testing.T) {
	proxySrv := httptest.NewServer(newHandler(t, freePort(t), Config{}))
	defer proxySrv.Close()

	req, err := http.NewRequest(http.MethodGet, proxySrv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("HTML navigation: status = %d, want 503", resp.StatusCode)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("HTML navigation: Content-Type = %q, want text/html", resp.Header.Get("Content-Type"))
	}
	if !strings.Contains(string(body), `http-equiv="refresh"`) {
		t.Fatalf("HTML navigation: body lacks meta refresh: %q", body)
	}

	req, err = http.NewRequest(http.MethodGet, proxySrv.URL+"/api", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("non-HTML request: status = %d, want 503", resp.StatusCode)
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("non-HTML request: Content-Type = %q, want plain", resp.Header.Get("Content-Type"))
	}
	if strings.Contains(string(body), "refresh") {
		t.Fatalf("non-HTML request: got the HTML page: %q", body)
	}
}

func TestProxiesOnceUpstreamBecomesReady(t *testing.T) {
	port := freePort(t)
	h := newHandler(t, port, Config{})
	proxySrv := httptest.NewServer(h)
	defer proxySrv.Close()

	resp, err := http.Get(proxySrv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("before upstream: status = %d, want 503", resp.StatusCode)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	upstream := &httptest.Server{
		Listener: ln,
		Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "ready")
		})},
	}
	upstream.Start()
	defer upstream.Close()

	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err := http.Get(proxySrv.URL + "/")
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode == http.StatusOK && string(body) == "ready" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("upstream up but proxy still returns %d %q", resp.StatusCode, body)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestUpstreamDiesMidRunServesStartingPage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "alive")
	}))
	port := upstreamPort(t, upstream)

	// Long TTL so the probe cache still says "up" after the upstream
	// dies, forcing the request through ReverseProxy into ErrorHandler.
	h := newHandler(t, port, Config{ProbeTTL: time.Hour})
	proxySrv := httptest.NewServer(h)
	defer proxySrv.Close()

	resp, err := http.Get(proxySrv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("while upstream alive: status = %d, want 200", resp.StatusCode)
	}

	upstream.Close()

	req, err := http.NewRequest(http.MethodGet, proxySrv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/html")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("after upstream death: status = %d, want 503", resp.StatusCode)
	}
	if !strings.Contains(string(body), `http-equiv="refresh"`) {
		t.Fatalf("after upstream death: HTML navigation got %q, want the starting page", body)
	}

	req, err = http.NewRequest(http.MethodGet, proxySrv.URL+"/api", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("after upstream death (non-HTML): status = %d, want 503", resp.StatusCode)
	}
	if strings.Contains(string(body), "refresh") {
		t.Fatalf("after upstream death (non-HTML): got the HTML page: %q", body)
	}
}
