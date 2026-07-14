package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jellehuibregtse/marquee/internal/gitinfo"
	"github.com/jellehuibregtse/marquee/internal/proxy"
	"github.com/jellehuibregtse/marquee/internal/status"
)

// TestAllowHostFlagReachesGuard proves the whole chain: --allow-host is
// parsed, plumbed into proxy.Config, and honored by the internal mux's
// Host allowlist, while a host that is neither built-in nor supplied
// still gets a 403.
func TestAllowHostFlagReachesGuard(t *testing.T) {
	opts, err := parseArgs("marquee", []string{"--allow-host", "myapp.test", "--", "bin/dev"}, io.Discard)
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}

	handler := proxy.New(proxy.Config{InternalPort: 1, AllowHosts: opts.allowHosts})
	status.Register(handler.Internal(), status.Deps{
		Git: func() gitinfo.Snapshot { return gitinfo.Snapshot{Branch: "trunk"} },
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	cases := map[string]int{
		"myapp.test":     http.StatusOK,
		"myapp.test:456": http.StatusOK,
		"evil.com":       http.StatusForbidden,
	}
	for host, want := range cases {
		req, err := http.NewRequest(http.MethodGet, srv.URL+"/__marquee/status", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("Host %q: status = %d, want %d", host, resp.StatusCode, want)
		}
	}
}
