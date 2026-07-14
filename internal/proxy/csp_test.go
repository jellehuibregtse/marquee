package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestRelaxCSPValue(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "script-src without self gains self, order and other tokens preserved",
			in:   "script-src http://assets.example:3000 'unsafe-inline'; connect-src 'self'",
			want: "script-src http://assets.example:3000 'unsafe-inline' 'self'; connect-src 'self'",
		},
		{
			name: "script-src already has self is idempotent",
			in:   "script-src 'self' https://cdn.example; connect-src 'self'",
			want: "script-src 'self' https://cdn.example; connect-src 'self'",
		},
		{
			name: "script-src-elem governs scripts and gets self, script-src left as-is",
			in:   "script-src https://cdn.example; script-src-elem https://cdn.example; connect-src 'self'",
			want: "script-src https://cdn.example; script-src-elem https://cdn.example 'self'; connect-src 'self'",
		},
		{
			name: "script-src-elem already with self is idempotent, script-src untouched, no default-src so connect unchanged",
			in:   "script-src https://cdn.example; script-src-elem 'self'",
			want: "script-src https://cdn.example; script-src-elem 'self'",
		},
		{
			name: "default-src only adds explicit script-src and connect-src, default-src unchanged",
			in:   "default-src https://cdn.example",
			want: "default-src https://cdn.example; script-src https://cdn.example 'self'; connect-src https://cdn.example 'self'",
		},
		{
			name: "connect-src without self gains self; default-src also derives a script-src",
			in:   "default-src 'self'; connect-src https://api.example",
			want: "default-src 'self'; connect-src https://api.example 'self'; script-src 'self'",
		},
		{
			name: "connect-src with self is idempotent",
			in:   "script-src 'self'; connect-src 'self' https://api.example",
			want: "script-src 'self'; connect-src 'self' https://api.example",
		},
		{
			name: "script-src none becomes self",
			in:   "script-src 'none'; connect-src 'self'",
			want: "script-src 'self'; connect-src 'self'",
		},
		{
			name: "connect-src none becomes self",
			in:   "script-src 'self'; connect-src 'none'",
			want: "script-src 'self'; connect-src 'self'",
		},
		{
			name: "default-src none yields self-only derived directives, default-src stays none",
			in:   "default-src 'none'",
			want: "default-src 'none'; script-src 'self'; connect-src 'self'",
		},
		{
			name: "no script or connect governing directive is left unchanged",
			in:   "img-src https://cdn.example; style-src 'unsafe-inline'",
			want: "img-src https://cdn.example; style-src 'unsafe-inline'",
		},
		{
			name: "directive names are case-insensitive",
			in:   "Script-Src http://assets.example:3000; Connect-Src http://api.example",
			want: "Script-Src http://assets.example:3000 'self'; Connect-Src http://api.example 'self'",
		},
		{
			name: "trailing separators and extra spaces normalize deterministically",
			in:   "script-src   http://assets.example:3000 ;  connect-src 'self' ;",
			want: "script-src http://assets.example:3000 'self'; connect-src 'self'",
		},
		{
			name: "empty value left unchanged",
			in:   "",
			want: "",
		},
		{
			name: "only separators left unchanged",
			in:   " ; ; ",
			want: " ; ; ",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := relaxCSPValue(tc.in); got != tc.want {
				t.Errorf("relaxCSPValue(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRelaxCSPForBarNoHeaderNoChange(t *testing.T) {
	h := http.Header{}
	relaxCSPForBar(h)
	if len(h.Values(cspHeader)) != 0 {
		t.Fatalf("expected no CSP header, got %v", h.Values(cspHeader))
	}
}

func TestRelaxCSPForBarLeavesReportOnlyUntouched(t *testing.T) {
	const reportOnly = "Content-Security-Policy-Report-Only"
	h := http.Header{}
	h.Set(reportOnly, "script-src http://assets.example:3000")
	relaxCSPForBar(h)
	if got := h.Get(reportOnly); got != "script-src http://assets.example:3000" {
		t.Fatalf("report-only CSP was modified: %q", got)
	}
	if len(h.Values(cspHeader)) != 0 {
		t.Fatalf("an enforcing CSP was created out of a report-only one: %v", h.Values(cspHeader))
	}
}

func TestRelaxCSPForBarRewritesAllEnforcingHeaders(t *testing.T) {
	h := http.Header{}
	h.Add(cspHeader, "script-src http://assets.example:3000")
	h.Add(cspHeader, "connect-src http://api.example")
	relaxCSPForBar(h)
	got := h.Values(cspHeader)
	want := []string{
		"script-src http://assets.example:3000 'self'",
		"connect-src http://api.example 'self'",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("multiple CSP headers not both rewritten\n got %q\nwant %q", got, want)
	}
}

func TestRelaxCSPForBarGarbageValueLeftUnchanged(t *testing.T) {
	h := http.Header{}
	h.Set(cspHeader, " ;; ")
	relaxCSPForBar(h)
	if got := h.Get(cspHeader); got != " ;; " {
		t.Fatalf("garbage CSP value was altered: %q", got)
	}
}

// cspProxyGet proxies a GET / through a real Handler built with cfg, against
// an upstream that returns body/contentType/status and every header in
// extraHeaders (used to plant a CSP). It returns the proxied response headers
// and body.
func cspProxyGet(t *testing.T, cfg Config, body, contentType string, status int, extraHeaders http.Header) (http.Header, string) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, values := range extraHeaders {
			for _, v := range values {
				w.Header().Add(name, v)
			}
		}
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(upstream.Close)

	proxySrv := httptest.NewServer(newHandler(t, upstreamPort(t, upstream), cfg))
	t.Cleanup(proxySrv.Close)

	resp, err := http.Get(proxySrv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.Header, string(got)
}

const cspPage = `<!doctype html><html><head><title>t</title></head><body><h1>hi</h1></body></html>`

func TestInjectRelaxesCSPWhenBarInjected(t *testing.T) {
	upstreamCSP := http.Header{cspHeader: {"script-src http://assets.example:3000 'unsafe-inline'; connect-src http://api.example"}}
	headers, body := cspProxyGet(t, Config{RelaxCSP: true}, cspPage, "text/html", http.StatusOK, upstreamCSP)

	if !bytes.Contains([]byte(body), []byte(barSnippet)) {
		t.Fatal("bar snippet not injected")
	}
	got := headers.Get(cspHeader)
	if !bytes.Contains([]byte(got), []byte("script-src http://assets.example:3000 'unsafe-inline' 'self'")) {
		t.Errorf("script-src did not gain 'self': %q", got)
	}
	if !bytes.Contains([]byte(got), []byte("connect-src http://api.example 'self'")) {
		t.Errorf("connect-src did not gain 'self': %q", got)
	}
}

func TestInjectLeavesCSPUnchangedWhenRelaxDisabled(t *testing.T) {
	const original = "script-src http://assets.example:3000; connect-src http://api.example"
	upstreamCSP := http.Header{cspHeader: {original}}
	headers, body := cspProxyGet(t, Config{RelaxCSP: false}, cspPage, "text/html", http.StatusOK, upstreamCSP)

	if !bytes.Contains([]byte(body), []byte(barSnippet)) {
		t.Fatal("bar snippet should still be injected with --keep-csp")
	}
	if got := headers.Get(cspHeader); got != original {
		t.Errorf("CSP changed with RelaxCSP=false: got %q, want %q", got, original)
	}
}

func TestInjectLeavesCSPUntouchedOnNonInjectedResponse(t *testing.T) {
	const original = "script-src http://assets.example:3000"
	upstreamCSP := http.Header{cspHeader: {original}}

	// JSON: not an injection candidate, so CSP must pass through verbatim
	// even though RelaxCSP is on.
	headers, body := cspProxyGet(t, Config{RelaxCSP: true}, `{"ok":true}`, "application/json", http.StatusOK, upstreamCSP)
	if bytes.Contains([]byte(body), []byte(barSnippet)) {
		t.Fatal("snippet injected into JSON")
	}
	if got := headers.Get(cspHeader); got != original {
		t.Errorf("CSP changed on a non-injected response: got %q, want %q", got, original)
	}

	// A non-2xx HTML response is also skipped: CSP untouched.
	headers, _ = cspProxyGet(t, Config{RelaxCSP: true}, cspPage, "text/html", http.StatusNotFound, upstreamCSP)
	if got := headers.Get(cspHeader); got != original {
		t.Errorf("CSP changed on a non-2xx response: got %q, want %q", got, original)
	}
}
