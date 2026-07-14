package proxy

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fixtureUpstream(body []byte, contentType string, status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if contentType == "" {
			// Assigning nil suppresses net/http's content sniffing, so the
			// response genuinely carries no Content-Type header.
			w.Header()["Content-Type"] = nil
		} else {
			w.Header().Set("Content-Type", contentType)
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}
}

func proxyGet(t *testing.T, upstreamHandler http.Handler, mutate func(*http.Request)) (*http.Response, []byte) {
	t.Helper()
	upstream := httptest.NewServer(upstreamHandler)
	t.Cleanup(upstream.Close)
	proxySrv := httptest.NewServer(newHandler(t, upstreamPort(t, upstream), Config{}))
	t.Cleanup(proxySrv.Close)

	req, err := http.NewRequest(http.MethodGet, proxySrv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

func TestInjectionGoldenFiles(t *testing.T) {
	tests := []struct {
		name        string
		fixture     string
		golden      string // empty: expect the fixture back byte-for-byte
		contentType string
		status      int
		mutate      func(*http.Request)
	}{
		{name: "normal document injected", fixture: "normal.html", golden: "normal.golden.html", contentType: "text/html", status: http.StatusOK},
		{name: "content type with charset injected", fixture: "normal.html", golden: "normal.golden.html", contentType: "text/html; charset=utf-8", status: http.StatusOK},
		{name: "uppercase closing tag injected", fixture: "uppercase.html", golden: "uppercase.golden.html", contentType: "text/html", status: http.StatusOK},
		{name: "multiple closers splice at last", fixture: "multiple-body.html", golden: "multiple-body.golden.html", contentType: "text/html", status: http.StatusOK},
		{name: "trailing script closer skipped", fixture: "trailing-script.html", golden: "trailing-script.golden.html", contentType: "text/html", status: http.StatusOK},
		{name: "trailing comment closer skipped", fixture: "trailing-comment.html", golden: "trailing-comment.golden.html", contentType: "text/html", status: http.StatusOK},
		{name: "textarea closer before real close", fixture: "textarea-body.html", golden: "textarea-body.golden.html", contentType: "text/html", status: http.StatusOK},
		{name: "only closer inside script skipped", fixture: "script-only-body.html", contentType: "text/html", status: http.StatusOK},
		{name: "no closing body tag skipped", fixture: "fragment.html", contentType: "text/html", status: http.StatusOK},
		{name: "json skipped", fixture: "data.json", contentType: "application/json", status: http.StatusOK},
		{name: "500 skipped", fixture: "normal.html", contentType: "text/html", status: http.StatusInternalServerError},
		{name: "missing content type skipped", fixture: "normal.html", contentType: "", status: http.StatusOK},
		{name: "iframe request skipped", fixture: "normal.html", contentType: "text/html", status: http.StatusOK,
			mutate: func(r *http.Request) { r.Header.Set("Sec-Fetch-Dest", "iframe") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := readFixture(t, tc.fixture)
			want := input
			if tc.golden != "" {
				want = readFixture(t, tc.golden)
			}

			resp, body := proxyGet(t, fixtureUpstream(input, tc.contentType, tc.status), tc.mutate)

			if resp.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.status)
			}
			if tc.contentType == "" && resp.Header.Get("Content-Type") != "" {
				t.Fatalf("Content-Type = %q, want it absent (sniffing crept in)", resp.Header.Get("Content-Type"))
			}
			if !bytes.Equal(body, want) {
				t.Errorf("body does not match golden:\ngot:\n%s\nwant:\n%s", body, want)
			}
			if cl := resp.Header.Get("Content-Length"); cl != strconv.Itoa(len(body)) {
				t.Errorf("Content-Length = %q, want %d (the bytes actually received)", cl, len(body))
			}
		})
	}
}

func TestInjectionRecomputesContentLengthForChunkedUpstream(t *testing.T) {
	input := readFixture(t, "normal.html")
	want := readFixture(t, "normal.golden.html")
	split := len(input) / 2

	resp, body := proxyGet(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(input[:split])
		w.(http.Flusher).Flush()
		_, _ = w.Write(input[split:])
	}), nil)

	if !bytes.Equal(body, want) {
		t.Errorf("body does not match golden:\ngot:\n%s\nwant:\n%s", body, want)
	}
	if cl := resp.Header.Get("Content-Length"); cl != strconv.Itoa(len(want)) {
		t.Errorf("Content-Length = %q, want %d", cl, len(want))
	}
}

func hugeHTML(t *testing.T) []byte {
	t.Helper()
	head := "<!doctype html>\n<html>\n<head><title>huge</title></head>\n<body>\n"
	line := "<p>synthetic padding line for the size cap test</p>\n"
	tail := "</body>\n</html>\n"
	repeats := (injectSizeCap+(1<<20))/len(line) + 1
	doc := head + strings.Repeat(line, repeats) + tail
	if len(doc) <= injectSizeCap {
		t.Fatalf("generated document is %d bytes, need > %d", len(doc), injectSizeCap)
	}
	return []byte(doc)
}

func TestHugeBodyWithContentLengthPassesThrough(t *testing.T) {
	input := hugeHTML(t)
	resp, body := proxyGet(t, fixtureUpstream(input, "text/html", http.StatusOK), nil)

	if !bytes.Equal(body, input) {
		t.Fatalf("huge body altered in transit: got %d bytes, want %d identical bytes", len(body), len(input))
	}
	if cl := resp.Header.Get("Content-Length"); cl != strconv.Itoa(len(input)) {
		t.Fatalf("Content-Length = %q, want %d", cl, len(input))
	}
}

func TestHugeChunkedBodyPassesThrough(t *testing.T) {
	// No Content-Length, so the injector cannot know the size up front: it
	// buffers until the cap, then must deliver the prefix plus the rest of
	// the stream untouched.
	input := hugeHTML(t)
	split := injectSizeCap / 2

	_, body := proxyGet(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(input[:split])
		w.(http.Flusher).Flush()
		_, _ = w.Write(input[split:])
	}), nil)

	if !bytes.Equal(body, input) {
		t.Fatalf("huge chunked body altered in transit: got %d bytes, want %d identical bytes", len(body), len(input))
	}
	if bytes.Contains(body, []byte(barSnippet)) {
		t.Fatal("snippet injected into an over-cap body")
	}
}

func TestEventStreamSkippedAndNotBuffered(t *testing.T) {
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
		t.Fatalf("reading first event before stream close (injector buffered the stream?): %v", err)
	}
	if first != "data: first\n" {
		t.Fatalf("first event = %q, want %q", first, "data: first\n")
	}
	close(release)
	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(first+string(rest), barSnippet) {
		t.Fatal("snippet injected into an event stream")
	}
}

func TestTruncatedUpstreamBodyFailsOpen(t *testing.T) {
	// The upstream promises 4096 bytes but closes after a short HTML
	// prefix, so the injector's buffered read errors mid-body. Fail-open
	// means the client still receives exactly the bytes the upstream sent,
	// followed by the same broken connection it would have seen without
	// marquee — never a marquee-fabricated response.
	prefix := "<html><body>truncated"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: text/html\r\nContent-Length: 4096\r\n\r\n" + prefix)
		_ = buf.Flush()
	}))
	defer upstream.Close()

	proxySrv := httptest.NewServer(newHandler(t, upstreamPort(t, upstream), Config{}))
	defer proxySrv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(proxySrv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := string(body); got != prefix {
		t.Fatalf("body = %q, want the original prefix %q", got, prefix)
	}
	if readErr == nil {
		t.Fatal("read completed cleanly, want the upstream truncation to surface as it would without marquee")
	}
}

func TestCompressedResponsePassesThrough(t *testing.T) {
	plain := readFixture(t, "normal.html")
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	compressed := gz.Bytes()

	resp, body := proxyGet(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", strconv.Itoa(len(compressed)))
		_, _ = w.Write(compressed)
	}), func(r *http.Request) {
		// An explicit Accept-Encoding stops the test client's transport
		// from transparently gunzipping, so the assertion sees the exact
		// bytes the proxy sent.
		r.Header.Set("Accept-Encoding", "gzip")
	})

	if !bytes.Equal(body, compressed) {
		t.Fatalf("compressed body altered in transit: got %d bytes, want %d identical bytes", len(body), len(compressed))
	}
	if cl := resp.Header.Get("Content-Length"); cl != strconv.Itoa(len(compressed)) {
		t.Fatalf("Content-Length = %q, want %d", cl, len(compressed))
	}
	if enc := resp.Header.Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("Content-Encoding = %q, want %q", enc, "gzip")
	}
}

func TestContentEncodingGate(t *testing.T) {
	tests := []struct {
		encodings []string
		want      bool
	}{
		{nil, true},
		{[]string{"identity"}, true},
		{[]string{"IDENTITY"}, true},
		{[]string{"gzip"}, false},
		{[]string{"br"}, false},
		{[]string{"deflate"}, false},
		{[]string{"gzip, identity"}, false},
		{[]string{"identity", "gzip"}, false},
	}
	for _, tc := range tests {
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/html"}},
		}
		for _, e := range tc.encodings {
			resp.Header.Add("Content-Encoding", e)
		}
		if got := isInjectionCandidate(resp); got != tc.want {
			t.Errorf("Content-Encoding %v: candidate = %v, want %v", tc.encodings, got, tc.want)
		}
	}
}

type countingCloser struct {
	io.Reader
	closes int
}

func (c *countingCloser) Close() error {
	c.closes++
	return nil
}

func TestCapOverrunSeamClosesUpstreamBodyOnce(t *testing.T) {
	input := hugeHTML(t)
	upstream := &countingCloser{Reader: bytes.NewReader(input)}
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		Header:        http.Header{"Content-Type": {"text/html"}},
		Body:          upstream,
		ContentLength: -1,
	}
	in := newInjector(log.New(io.Discard, "", 0), newBarSwitches(false))
	if err := in.modifyResponse(resp); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, input) {
		t.Fatalf("seam delivered %d bytes, want %d identical bytes", len(got), len(input))
	}
	// ReverseProxy closes the response body exactly once after copying it.
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if upstream.closes != 1 {
		t.Fatalf("upstream body closed %d times, want exactly once", upstream.closes)
	}
}

type panickingBody struct{ value any }

func (p panickingBody) Read([]byte) (int, error) { panic(p.value) }
func (p panickingBody) Close() error             { return nil }

func TestErrAbortHandlerPanicNotSwallowed(t *testing.T) {
	in := newInjector(log.New(io.Discard, "", 0), newBarSwitches(false))
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		Header:        http.Header{"Content-Type": {"text/html"}},
		Body:          panickingBody{http.ErrAbortHandler},
		ContentLength: -1,
	}
	recovered := func() (r any) {
		defer func() { r = recover() }()
		_ = in.modifyResponse(resp)
		return nil
	}()
	if recovered != http.ErrAbortHandler {
		t.Fatalf("recovered %v, want http.ErrAbortHandler to keep propagating", recovered)
	}
}

func TestStructuralBodyClose(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"none", "<html><p>no closer</p></html>", -1},
		{"plain", "<body>x</body>", 7},
		{"uppercase", "<BODY>x</BODY></HTML>", 7},
		{"last real wins", "<body></body><body>x</body></html>", 20},
		{"closer inside script skipped", `<body>x<script>t="</body>"</script></body>`, 35},
		{"only closer inside script", `<body><script>t="</body>"</script>`, -1},
		{"only closer inside comment", "<body><!-- </body> --></body-not>", -1},
		{"trailing script after real close", "<body>x</body></html><script>\"</body>\"</script>", 7},
		{"trailing comment after real close", "<body>x</body></html><!-- </body> -->", 7},
		{"closer inside textarea before real", "<body><textarea></body></textarea></body>", 34},
		{"unterminated script hides later closer", "<body></body><script></body>", 6},
		{"unterminated comment hides later closer", "<body></body><!-- </body>", 6},
		{"script prefix not a script element", "<body><scriptless></body>", 18},
		{"double-escaped script falls open to real close", "<body>x</body>\n<script>\n<!--\n<script>\n</script>\n</body>\n</script>\n", 7},
		{"double-escaped script with no earlier close skipped", "<body>\n<script>\n<!--\n<script>\n</script>\n</body>\n</script>\n", -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := structuralBodyClose([]byte(tc.in)); got != tc.want {
				t.Errorf("structuralBodyClose(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestInternalPathNeverCandidate(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://localhost/__marquee/status", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/html"}},
		Request:    req,
	}
	if isInjectionCandidate(resp) {
		t.Fatal("internal path considered an injection candidate")
	}
}
