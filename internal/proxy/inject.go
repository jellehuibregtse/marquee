package proxy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// barSnippet is spliced immediately before the final </body> of injectable
// HTML documents. The /__marquee/bar.js path is the contract with the bar
// task, which serves the script from the internal mux.
const barSnippet = `<script type="module" src="/__marquee/bar.js"></script><marquee-bar></marquee-bar>`

// injectSizeCap is the largest body the injector will buffer. Anything
// bigger passes through untouched — and unbuffered when Content-Length
// announces the size up front.
const injectSizeCap = 10 << 20

// injector splices barSnippet into proxied HTML documents. Fail-open is a
// law here: every failure mode delivers the original upstream bytes to the
// client, and errors are logged once per distinct message (the gitinfo
// discipline), never surfaced as a proxy error.
type injector struct {
	logger   *log.Logger
	switches *barSwitches

	mu      sync.Mutex
	lastMsg string
}

func newInjector(logger *log.Logger, switches *barSwitches) *injector {
	return &injector{logger: logger, switches: switches}
}

// modifyResponse is the ReverseProxy.ModifyResponse hook. It always returns
// nil: returning an error would route the response into ErrorHandler and
// replace a working upstream response, which fail-open forbids.
func (in *injector) modifyResponse(resp *http.Response) error {
	if !in.switches.allows(resp.Request) {
		return nil
	}
	if !isInjectionCandidate(resp) || resp.ContentLength > injectSizeCap {
		return nil
	}
	in.inject(resp)
	return nil
}

// isInjectionCandidate decides from headers alone — before any body byte is
// read — so non-candidates (SSE streams, JSON, errors) keep streaming under
// FlushInterval: -1 without ever being buffered.
func isInjectionCandidate(resp *http.Response) bool {
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return false
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.HasPrefix(contentType, "text/event-stream") {
		return false
	}
	if !strings.HasPrefix(contentType, "text/html") {
		return false
	}
	if !isIdentityEncoding(resp.Header) {
		return false
	}
	if req := resp.Request; req != nil {
		if req.URL != nil && isInternalPath(req.URL.Path) {
			return false
		}
		if req.Header.Get("Sec-Fetch-Dest") == "iframe" {
			return false
		}
	}
	return true
}

// isIdentityEncoding reports whether the response body is plainly
// uncompressed. The proxy forces Accept-Encoding: identity upstream, but a
// misbehaving upstream can still answer compressed — splicing into those
// bytes would corrupt the page, so anything not identity passes through.
func isIdentityEncoding(h http.Header) bool {
	for _, value := range h.Values("Content-Encoding") {
		for _, token := range strings.Split(value, ",") {
			token = strings.ToLower(strings.TrimSpace(token))
			if token != "" && token != "identity" {
				return false
			}
		}
	}
	return true
}

func (in *injector) inject(resp *http.Response) {
	upstream := resp.Body
	var (
		buf      []byte
		complete bool
		readErr  error
	)
	// restore points resp.Body at the original bytes: the buffered prefix,
	// then whatever the upstream still holds (or its read error). Valid at
	// every stage, so the recover below can always fail open.
	restore := func() {
		readers := []io.Reader{bytes.NewReader(buf)}
		switch {
		case readErr != nil:
			readers = append(readers, errorReader{readErr})
		case !complete:
			readers = append(readers, upstream)
		}
		resp.Body = readCloser{io.MultiReader(readers...), upstream}
	}
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		// http.ErrAbortHandler is the server's own abort signal and must
		// keep propagating; swallowing it would turn an aborted response
		// into a half-written one.
		if err, ok := r.(error); ok && errors.Is(err, http.ErrAbortHandler) {
			panic(r)
		}
		in.logOnce("inject: recovered panic, passing original response through: %v", r)
		restore()
	}()

	buf, complete, readErr = readCapped(upstream, injectSizeCap)
	if readErr != nil {
		in.logOnce("inject: reading upstream body, passing original bytes through: %v", readErr)
		restore()
		return
	}
	if !complete {
		restore()
		return
	}

	idx := structuralBodyClose(buf)
	if idx < 0 {
		restore()
		return
	}

	spliced := make([]byte, 0, len(buf)+len(barSnippet))
	spliced = append(spliced, buf[:idx]...)
	spliced = append(spliced, barSnippet...)
	spliced = append(spliced, buf[idx:]...)
	resp.Body = readCloser{bytes.NewReader(spliced), upstream}
	resp.ContentLength = int64(len(spliced))
	resp.Header.Set("Content-Length", strconv.Itoa(len(spliced)))
}

// readCapped reads r until EOF or one byte past limit. complete reports
// whether the whole body fit within limit.
func readCapped(r io.Reader, limit int64) (buf []byte, complete bool, err error) {
	buf, err = io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return buf, false, err
	}
	return buf, int64(len(buf)) <= limit, nil
}

// structuralBodyClose returns the index of the document's closing </body> —
// the last case-insensitive </body> that lies in ordinary markup rather than
// inside a <script> element or an HTML comment. A </body> that appears only in
// a script's string literal or a comment (whether before or after the real
// close) is not the document end, so it is skipped. When every occurrence is
// inside such a region, or there is none, the result is -1 and the caller
// passes the body through untouched. This keeps a full HTML parser out of the
// hot path: a single forward scan, skipping the two regions that can carry a
// literal </body> without meaning it.
func structuralBodyClose(b []byte) int {
	last := -1
	for i := 0; i < len(b); {
		switch {
		case startsFold(b[i:], "<!--"):
			end := indexFold(b[i+len("<!--"):], "-->")
			if end < 0 {
				return last
			}
			i += len("<!--") + end + len("-->")
		case isScriptOpen(b, i):
			gt := bytes.IndexByte(b[i:], '>')
			if gt < 0 {
				return last
			}
			content := i + gt + 1
			end := indexFold(b[content:], "</script>")
			if end < 0 {
				return last
			}
			// A nested "<script" inside script content triggers HTML's
			// double-escaped tokenizer state, where the first </script>
			// does not close the element — so our region end diverges from
			// the browser's. Rather than risk anchoring inside a still-open
			// script, fail open at the last known-good structural close.
			if indexFold(b[content:content+end], "<script") >= 0 {
				return last
			}
			i = content + end + len("</script>")
		case startsFold(b[i:], "</body>"):
			last = i
			i += len("</body>")
		default:
			i++
		}
	}
	return last
}

// isScriptOpen reports whether b at i begins a <script> start tag. The byte
// after "<script" must end the tag name (>, /, or whitespace) so that
// "<scriptfoo" is not mistaken for a script element.
func isScriptOpen(b []byte, i int) bool {
	if !startsFold(b[i:], "<script") {
		return false
	}
	next := i + len("<script")
	if next >= len(b) {
		return true
	}
	switch b[next] {
	case '>', '/', ' ', '\t', '\n', '\r', '\f':
		return true
	default:
		return false
	}
}

// startsFold reports whether b begins with prefix, case-insensitively.
func startsFold(b []byte, prefix string) bool {
	return len(b) >= len(prefix) && bytes.EqualFold(b[:len(prefix)], []byte(prefix))
}

// indexFold returns the index of the first case-insensitive occurrence of sub
// in b, or -1.
func indexFold(b []byte, sub string) int {
	s := []byte(sub)
	for i := 0; i+len(s) <= len(b); i++ {
		if bytes.EqualFold(b[i:i+len(s)], s) {
			return i
		}
	}
	return -1
}

func (in *injector) logOnce(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	in.mu.Lock()
	defer in.mu.Unlock()
	if msg == in.lastMsg {
		return
	}
	in.lastMsg = msg
	in.logger.Printf("marquee: %s", msg)
}

type readCloser struct {
	io.Reader
	io.Closer
}

type errorReader struct{ err error }

func (e errorReader) Read([]byte) (int, error) { return 0, e.err }
