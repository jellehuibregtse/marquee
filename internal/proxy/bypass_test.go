package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestShouldInjectTruthTable(t *testing.T) {
	tests := []struct {
		envDisabled, toggleOff, headerSkip bool
		want                               bool
	}{
		{false, false, false, true},
		{false, false, true, false},
		{false, true, false, false},
		{false, true, true, false},
		{true, false, false, false},
		{true, false, true, false},
		{true, true, false, false},
		{true, true, true, false},
	}
	for _, tc := range tests {
		got := shouldInject(tc.envDisabled, tc.toggleOff, tc.headerSkip)
		if got != tc.want {
			t.Errorf("shouldInject(env=%v, toggle=%v, header=%v) = %v, want %v",
				tc.envDisabled, tc.toggleOff, tc.headerSkip, got, tc.want)
		}
	}
}

// bypassProxy serves normal.html from a fixture upstream through one
// long-lived proxy handler, so toggle state persists across requests
// within a test.
func bypassProxy(t *testing.T) *httptest.Server {
	t.Helper()
	input := readFixture(t, "normal.html")
	upstream := httptest.NewServer(fixtureUpstream(input, "text/html", http.StatusOK))
	t.Cleanup(upstream.Close)
	proxySrv := httptest.NewServer(newHandler(t, upstreamPort(t, upstream), Config{}))
	t.Cleanup(proxySrv.Close)
	return proxySrv
}

func get(t *testing.T, url string, mutate func(*http.Request)) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
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
	return resp, string(body)
}

func assertInjected(t *testing.T, body string, want bool) {
	t.Helper()
	if got := strings.Contains(body, barSnippet); got != want {
		t.Fatalf("body contains bar snippet = %v, want %v:\n%s", got, want, body)
	}
}

func TestHeaderSkipDisablesInjectionForItsRequestOnly(t *testing.T) {
	proxySrv := bypassProxy(t)

	_, body := get(t, proxySrv.URL+"/", func(r *http.Request) {
		r.Header.Set("X-Marquee", "skip")
	})
	assertInjected(t, body, false)

	_, body = get(t, proxySrv.URL+"/", nil)
	assertInjected(t, body, true)
}

func TestHeaderSkipValueIsCaseInsensitiveAndExact(t *testing.T) {
	proxySrv := bypassProxy(t)

	_, body := get(t, proxySrv.URL+"/", func(r *http.Request) {
		r.Header.Set("X-Marquee", "SKIP")
	})
	assertInjected(t, body, false)

	// Only "skip" disables injection; other values are stripped upstream
	// but do not bypass.
	_, body = get(t, proxySrv.URL+"/", func(r *http.Request) {
		r.Header.Set("X-Marquee", "something-else")
	})
	assertInjected(t, body, true)

	// A skip anywhere among multiple X-Marquee values still counts.
	_, body = get(t, proxySrv.URL+"/", func(r *http.Request) {
		r.Header.Add("X-Marquee", "trace")
		r.Header.Add("X-Marquee", "skip")
	})
	assertInjected(t, body, false)
}

func TestXMarqueeHeaderNeverForwardedUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values("X-Marquee")
		_, _ = fmt.Fprintf(w, "count=%d values=%s", len(values), strings.Join(values, ","))
	}))
	t.Cleanup(upstream.Close)
	proxySrv := httptest.NewServer(newHandler(t, upstreamPort(t, upstream), Config{}))
	t.Cleanup(proxySrv.Close)

	for _, value := range []string{"skip", "anything"} {
		_, body := get(t, proxySrv.URL+"/", func(r *http.Request) {
			r.Header.Set("X-Marquee", value)
		})
		if body != "count=0 values=" {
			t.Errorf("X-Marquee: %s reached the upstream: %s", value, body)
		}
	}
}

func TestEnvDisablesInjectionAndToggleCannotReenable(t *testing.T) {
	t.Setenv("MARQUEE_DISABLE_BAR", "1")
	proxySrv := bypassProxy(t)

	_, body := get(t, proxySrv.URL+"/", nil)
	assertInjected(t, body, false)

	resp, body := get(t, proxySrv.URL+"/__marquee/toggle?bar=on", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("toggle status = %d, want 200", resp.StatusCode)
	}
	if !strings.HasPrefix(body, "bar: off\n") {
		t.Fatalf("toggle reported %q, want it to report the bar off (env is hard off)", body)
	}
	if !strings.Contains(body, "MARQUEE_DISABLE_BAR") {
		t.Fatalf("toggle response %q lacks the env override hint", body)
	}

	_, body = get(t, proxySrv.URL+"/", nil)
	assertInjected(t, body, false)
}

func TestToggleFlipsInjectionSessionWide(t *testing.T) {
	proxySrv := bypassProxy(t)

	resp, body := get(t, proxySrv.URL+"/__marquee/toggle", nil)
	if resp.StatusCode != http.StatusOK || body != "bar: on\n" {
		t.Fatalf("initial state: status = %d body = %q, want 200 %q", resp.StatusCode, body, "bar: on\n")
	}

	_, body = get(t, proxySrv.URL+"/__marquee/toggle?bar=off", nil)
	if body != "bar: off\n" {
		t.Fatalf("bar=off confirmed %q, want %q", body, "bar: off\n")
	}
	_, body = get(t, proxySrv.URL+"/", nil)
	assertInjected(t, body, false)

	_, body = get(t, proxySrv.URL+"/__marquee/toggle", nil)
	if body != "bar: off\n" {
		t.Fatalf("state after bar=off reported %q, want %q", body, "bar: off\n")
	}

	_, body = get(t, proxySrv.URL+"/__marquee/toggle?bar=on", nil)
	if body != "bar: on\n" {
		t.Fatalf("bar=on confirmed %q, want %q", body, "bar: on\n")
	}
	_, body = get(t, proxySrv.URL+"/", nil)
	assertInjected(t, body, true)
}

func TestToggleInvalidParamRejected(t *testing.T) {
	proxySrv := bypassProxy(t)

	for _, query := range []string{"?bar=banana", "?bar=", "?bar=ON"} {
		resp, body := get(t, proxySrv.URL+"/__marquee/toggle"+query, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", query, resp.StatusCode)
		}
		if !strings.Contains(body, "usage:") {
			t.Errorf("%s: body %q lacks a usage hint", query, body)
		}
	}

	_, body := get(t, proxySrv.URL+"/", nil)
	assertInjected(t, body, true)
}

func setSecFetchSite(value string) func(*http.Request) {
	return func(r *http.Request) {
		r.Header.Set("Sec-Fetch-Site", value)
	}
}

func TestToggleRejectsCrossOriginStateChange(t *testing.T) {
	for _, site := range []string{"cross-site", "same-site"} {
		t.Run(site, func(t *testing.T) {
			proxySrv := bypassProxy(t)

			resp, _ := get(t, proxySrv.URL+"/__marquee/toggle?bar=off", setSecFetchSite(site))
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("bar=off from %s: status = %d, want 403", site, resp.StatusCode)
			}

			// The rejected request must not have flipped the toggle.
			_, body := get(t, proxySrv.URL+"/__marquee/toggle", nil)
			if body != "bar: on\n" {
				t.Fatalf("state after rejected %s toggle = %q, want %q", site, body, "bar: on\n")
			}
			_, body = get(t, proxySrv.URL+"/", nil)
			assertInjected(t, body, true)
		})
	}
}

func TestToggleCrossSiteCannotForceBarOn(t *testing.T) {
	proxySrv := bypassProxy(t)

	// Put the bar off first via a legitimate typed navigation.
	_, body := get(t, proxySrv.URL+"/__marquee/toggle?bar=off", setSecFetchSite("none"))
	if body != "bar: off\n" {
		t.Fatalf("same-origin bar=off confirmed %q, want %q", body, "bar: off\n")
	}

	// A cross-site page cannot force it back on either.
	resp, _ := get(t, proxySrv.URL+"/__marquee/toggle?bar=on", setSecFetchSite("cross-site"))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site bar=on: status = %d, want 403", resp.StatusCode)
	}
	_, body = get(t, proxySrv.URL+"/__marquee/toggle", nil)
	if body != "bar: off\n" {
		t.Fatalf("state after rejected cross-site bar=on = %q, want %q", body, "bar: off\n")
	}
}

func TestToggleAllowsSameOriginAndDirectStateChange(t *testing.T) {
	for name, mutate := range map[string]func(*http.Request){
		"same-origin": setSecFetchSite("same-origin"),
		"typed-nav":   setSecFetchSite("none"),
		"curl":        nil,
	} {
		t.Run(name, func(t *testing.T) {
			proxySrv := bypassProxy(t)

			resp, body := get(t, proxySrv.URL+"/__marquee/toggle?bar=off", mutate)
			if resp.StatusCode != http.StatusOK || body != "bar: off\n" {
				t.Fatalf("%s bar=off: status = %d body = %q, want 200 %q", name, resp.StatusCode, body, "bar: off\n")
			}
			_, body = get(t, proxySrv.URL+"/", nil)
			assertInjected(t, body, false)
		})
	}
}

func TestToggleNoParamReportsAcrossOrigins(t *testing.T) {
	proxySrv := bypassProxy(t)

	// A pure state report mutates nothing, so it stays open even cross-site.
	resp, body := get(t, proxySrv.URL+"/__marquee/toggle", setSecFetchSite("cross-site"))
	if resp.StatusCode != http.StatusOK || body != "bar: on\n" {
		t.Fatalf("cross-site state report: status = %d body = %q, want 200 %q", resp.StatusCode, body, "bar: on\n")
	}
}

func TestToggleInvalidParamRejectedRegardlessOfOrigin(t *testing.T) {
	proxySrv := bypassProxy(t)

	resp, body := get(t, proxySrv.URL+"/__marquee/toggle?bar=maybe", setSecFetchSite("cross-site"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("cross-site bar=maybe: status = %d, want 400", resp.StatusCode)
	}
	if !strings.Contains(body, "usage:") {
		t.Fatalf("cross-site bar=maybe: body %q lacks a usage hint", body)
	}

	// An invalid value never mutates, whatever the origin.
	_, body = get(t, proxySrv.URL+"/", nil)
	assertInjected(t, body, true)
}

func TestToggleGuardedByInternalMux(t *testing.T) {
	proxySrv := bypassProxy(t)

	resp, _ := get(t, proxySrv.URL+"/__marquee/toggle?bar=off", func(r *http.Request) {
		r.Host = "evil.com"
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("Host evil.com: status = %d, want 403", resp.StatusCode)
	}

	// The forbidden request must not have flipped the toggle.
	_, body := get(t, proxySrv.URL+"/", nil)
	assertInjected(t, body, true)

	resp, _ = get(t, proxySrv.URL+"/__marquee/toggle", nil)
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want %q", cc, "no-store")
	}
}

func TestToggleMethodNotAllowed(t *testing.T) {
	proxySrv := bypassProxy(t)

	resp, err := http.Post(proxySrv.URL+"/__marquee/toggle?bar=off", "text/plain", bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", resp.StatusCode)
	}
}

func TestToggleConcurrentFlipsRaceClean(t *testing.T) {
	proxySrv := bypassProxy(t)

	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				state := "on"
				if (worker+i)%2 == 0 {
					state = "off"
				}
				resp, err := http.Get(proxySrv.URL + "/__marquee/toggle?bar=" + state)
				if err != nil {
					t.Error(err)
					return
				}
				_ = resp.Body.Close()
				resp, err = http.Get(proxySrv.URL + "/")
				if err != nil {
					t.Error(err)
					return
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}(worker)
	}
	wg.Wait()
}
