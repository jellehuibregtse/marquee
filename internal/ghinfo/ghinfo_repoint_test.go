package ghinfo

import (
	"path/filepath"
	"testing"
	"time"
)

// TestRepointChangesLookupDirectory proves Repoint moves the PR lookup to a
// new worktree: a fake gh that answers based on its working directory's base
// name reports a different PR after the repoint.
func TestRepointChangesLookupDirectory(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	baseA := filepath.Base(dirA)
	baseB := filepath.Base(dirB)
	script := `case "$(basename "$(pwd)")" in
` + baseA + `) echo '{"number":1,"title":"A","url":"u"}';;
` + baseB + `) echo '{"number":2,"title":"B","url":"u"}';;
*) exit 1;;
esac`
	gh := fakeGH(t, script)

	p := New(dirA, WithExecutable(gh), WithInterval(10_000_000), WithTimeout(time.Second))
	defer p.Stop()
	waitFor(t, "PR from dirA", func() bool { pr := p.PR(); return pr != nil && pr.Number == 1 })

	p.Repoint(dirB)
	waitFor(t, "PR from dirB after repoint", func() bool { pr := p.PR(); return pr != nil && pr.Number == 2 })
}
