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

	idx := lastBodyClose(buf)
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

// lastBodyClose returns the index of the last case-insensitive </body> in b,
// or -1. Scanning from the end keeps earlier occurrences (JS strings,
// comments) from attracting the splice.
func lastBodyClose(b []byte) int {
	tag := []byte("</body>")
	for i := len(b) - len(tag); i >= 0; i-- {
		if bytes.EqualFold(b[i:i+len(tag)], tag) {
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
