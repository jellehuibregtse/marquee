package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestLoggerQuietSuppressesInfoNotWarningsOrErrors(t *testing.T) {
	var buf bytes.Buffer
	log := newLogger(&buf, true)
	log.Info("listening on %s", "127.0.0.1:3000")
	log.Warn("could not open browser")
	log.Error("boom: %d", 1)

	out := buf.String()
	if strings.Contains(out, "listening on") {
		t.Errorf("quiet logger emitted an info line: %q", out)
	}
	if !strings.Contains(out, "could not open browser") {
		t.Errorf("quiet logger suppressed a warning: %q", out)
	}
	if !strings.Contains(out, "boom: 1") {
		t.Errorf("quiet logger suppressed an error: %q", out)
	}
}

func TestLoggerVerbosePrintsInfo(t *testing.T) {
	var buf bytes.Buffer
	log := newLogger(&buf, false)
	log.Info("listening on %s", "127.0.0.1:3000")

	out := buf.String()
	if !strings.Contains(out, "marquee: listening on 127.0.0.1:3000") {
		t.Errorf("non-quiet logger dropped info line: %q", out)
	}
}
