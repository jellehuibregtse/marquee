package hook

// The bounds a Runner ends up with are asserted white-box on purpose: reading
// them off a real run would mean a test that waits out the default idle timeout,
// which is minutes by design.

import (
	"testing"
	"time"
)

// The default has to survive a bootstrap step that says nothing for a while and
// finishes anyway: the first version of this bound killed a healthy dev stack
// after two minutes of silence from a step that normally takes seconds. It also
// has to stay under the ceiling, or it could never fire and the ceiling would be
// the only bound left.
func TestTheIdleDefaultOutlastsAnUnexplainedSilence(t *testing.T) {
	if DefaultIdleTimeout < 5*time.Minute {
		t.Errorf("DefaultIdleTimeout = %s, too short for a bootstrap step that goes quiet while it works", DefaultIdleTimeout)
	}
	if DefaultIdleTimeout >= DefaultTimeout {
		t.Errorf("DefaultIdleTimeout = %s is not under the %s ceiling, so it could never fire", DefaultIdleTimeout, DefaultTimeout)
	}
}

func TestAnUnconfiguredRunnerUsesTheDefaultBounds(t *testing.T) {
	r := New(Config{Command: "true"})
	if r.idle != DefaultIdleTimeout {
		t.Errorf("idle = %s, want the default %s", r.idle, DefaultIdleTimeout)
	}
	if r.timeout != DefaultTimeout {
		t.Errorf("timeout = %s, want the default %s", r.timeout, DefaultTimeout)
	}
}

func TestConfiguredBoundsWin(t *testing.T) {
	r := New(Config{Command: "true", IdleTimeout: 90 * time.Second, Timeout: 20 * time.Minute})
	if r.idle != 90*time.Second {
		t.Errorf("idle = %s, want the configured 1m30s", r.idle)
	}
	if r.timeout != 20*time.Minute {
		t.Errorf("timeout = %s, want the configured 20m", r.timeout)
	}
}
