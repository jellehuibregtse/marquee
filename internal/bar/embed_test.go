package bar

import (
	"strings"
	"testing"
)

func TestBarScriptEmbedded(t *testing.T) {
	data, err := Assets.ReadFile("bar.js")
	if err != nil {
		t.Fatalf("read embedded bar.js: %v", err)
	}
	js := string(data)
	if len(js) == 0 {
		t.Fatal("embedded bar.js is empty")
	}

	for _, marker := range []string{
		"customElements.define",
		"marquee-bar",
		`role="status"`,
		"/__marquee/status",
		`rel="noreferrer"`,
	} {
		if !strings.Contains(js, marker) {
			t.Errorf("bar.js missing expected marker %q", marker)
		}
	}

	for _, forbidden := range []string{
		"eval(",
		"http://",
		"https://",
		"import(",
	} {
		if strings.Contains(js, forbidden) {
			t.Errorf("bar.js contains forbidden pattern %q", forbidden)
		}
	}
}
