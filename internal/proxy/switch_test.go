package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jellehuibregtse/marquee/internal/switching"
)

// fakeSwitchSource is a test SwitchSource whose reported slug can be swapped
// atomically, standing in for the orchestrator's live Progress().
type fakeSwitchSource struct{ slug atomic.Value }

func (f *fakeSwitchSource) set(slug string) { f.slug.Store(slug) }

func (f *fakeSwitchSource) Progress() switching.Progress {
	slug, _ := f.slug.Load().(string)
	phase := switching.Idle
	if slug != "" {
		phase = switching.Booting
	}
	return switching.Progress{Phase: phase, Slug: slug}
}

func TestBarSnippetForToken(t *testing.T) {
	if got := barSnippetForToken(""); got != barSnippet {
		t.Errorf("empty token snippet = %q, want the token-less barSnippet", got)
	}
	got := barSnippetForToken("deadbeef")
	if !strings.Contains(got, `<marquee-bar token="deadbeef"></marquee-bar>`) {
		t.Errorf("snippet %q missing token attribute", got)
	}
	if !strings.HasPrefix(got, barScriptTag) {
		t.Errorf("snippet %q lost the script tag", got)
	}
}

func TestInjectCarriesSwitchToken(t *testing.T) {
	input := readFixture(t, "normal.html")
	upstream := httptest.NewServer(fixtureUpstream(input, "text/html", http.StatusOK))
	defer upstream.Close()

	proxySrv := httptest.NewServer(newHandler(t, upstreamPort(t, upstream), Config{SwitchToken: "cafef00d"}))
	defer proxySrv.Close()

	resp, err := http.Get(proxySrv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	// Token-stable assertion: the element carries exactly the configured token,
	// and the rest of the snippet (the script tag) is unchanged, so the golden
	// fixtures stay valid for the token-less default.
	if !bytes.Contains(body, []byte(`<marquee-bar token="cafef00d"></marquee-bar>`)) {
		t.Fatalf("injected body missing tokened element:\n%s", body)
	}
	if !bytes.Contains(body, []byte(barScriptTag)) {
		t.Fatalf("injected body missing script tag:\n%s", body)
	}
}

func TestInjectWithoutTokenUsesTokenlessSnippet(t *testing.T) {
	input := readFixture(t, "normal.html")
	upstream := httptest.NewServer(fixtureUpstream(input, "text/html", http.StatusOK))
	defer upstream.Close()

	proxySrv := httptest.NewServer(newHandler(t, upstreamPort(t, upstream), Config{}))
	defer proxySrv.Close()

	resp, err := http.Get(proxySrv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte("token=")) {
		t.Fatalf("token attribute leaked with no SwitchToken configured:\n%s", body)
	}
	if !bytes.Contains(body, []byte(barSnippet)) {
		t.Fatalf("token-less snippet not injected:\n%s", body)
	}
}

func TestSwitchingPageServedWhileSwitching(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "app content")
	}))
	defer upstream.Close()

	h := newHandler(t, upstreamPort(t, upstream), Config{})
	src := &fakeSwitchSource{}
	src.set("")
	h.SetSwitchSource(src)
	proxySrv := httptest.NewServer(h)
	defer proxySrv.Close()

	htmlGet := func() (*http.Response, string) {
		req, err := http.NewRequest(http.MethodGet, proxySrv.URL+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Accept", "text/html")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		return resp, string(body)
	}

	// Idle: the app is proxied normally.
	if resp, body := htmlGet(); resp.StatusCode != http.StatusOK || !strings.Contains(body, "app content") {
		t.Fatalf("idle: status = %d body = %q, want 200 with app content", resp.StatusCode, body)
	}

	// Switching: HTML navigation gets the self-refreshing switching page.
	src.set("lantern")
	resp, body := htmlGet()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("switching HTML: status = %d, want 503", resp.StatusCode)
	}
	if !strings.Contains(body, `http-equiv="refresh"`) {
		t.Fatalf("switching HTML: body lacks meta refresh: %q", body)
	}
	if !strings.Contains(body, "Switching to lantern") {
		t.Fatalf("switching HTML: body does not name the target worktree: %q", body)
	}

	// A non-HTML request during a switch gets a plain 503, never the HTML page.
	req, err := http.NewRequest(http.MethodGet, proxySrv.URL+"/api", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	nonHTML, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("switching non-HTML: status = %d, want 503", resp.StatusCode)
	}
	if strings.Contains(string(nonHTML), "refresh") {
		t.Fatalf("switching non-HTML: got the HTML page: %q", nonHTML)
	}

	// Back to idle: proxying resumes.
	src.set("")
	if resp, body := htmlGet(); resp.StatusCode != http.StatusOK || !strings.Contains(body, "app content") {
		t.Fatalf("after switch: status = %d body = %q, want 200 with app content", resp.StatusCode, body)
	}
}

func TestSwitchingPageEscapesSlug(t *testing.T) {
	got := renderSwitching(`<img src=x>`)
	if strings.Contains(got, "<img src=x>") {
		t.Fatalf("slug not HTML-escaped: %q", got)
	}
	if !strings.Contains(got, "&lt;img src=x&gt;") {
		t.Fatalf("expected escaped slug in page: %q", got)
	}
}

func TestSwitchingProbeAbsentBehavesNormally(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "app content")
	}))
	defer upstream.Close()

	proxySrv := httptest.NewServer(newHandler(t, upstreamPort(t, upstream), Config{}))
	defer proxySrv.Close()

	resp, err := http.Get(proxySrv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "app content") {
		t.Fatalf("no probe set: status = %d body = %q, want 200 app content", resp.StatusCode, body)
	}
}
