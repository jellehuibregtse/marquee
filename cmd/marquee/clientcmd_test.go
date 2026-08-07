package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func mkdirAll(dir string) error { return os.MkdirAll(dir, 0o750) }

// chdir moves into dir for the test and back afterwards. The client subcommands
// read the working directory to decide which marquee they mean, so the tests
// have to be able to stand somewhere.
func chdir(t *testing.T, dir string) {
	t.Helper()
	before, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(before) })
}

// TestCheckSlugListsTheRealTargets is why the client validates a slug it is not
// the authority on: a typo is the likeliest mistake, and the endpoint's own
// "unknown_slug" leaves the caller with nothing to correct it by.
func TestCheckSlugListsTheRealTargets(t *testing.T) {
	target := targetServing("127.0.0.1:3000", "/repo/main", "/repo/feature")
	err := checkSlug(target, "featrue")
	if err == nil {
		t.Fatal("checkSlug accepted an unknown slug")
	}
	for _, want := range []string{`"featrue"`, "main", "feature"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if err := checkSlug(target, "feature"); err != nil {
		t.Errorf("checkSlug rejected a real target: %v", err)
	}
}

func TestReportSwitchFailureAdvisesTheNextStep(t *testing.T) {
	for _, tc := range []struct {
		result switchResult
		want   string
	}{
		{switchResult{Error: "dirty"}, "--force"},
		{switchResult{Error: "busy"}, "already in progress"},
		{switchResult{Error: "switch_failed", Message: "reverted to the previous worktree"}, "reverted to the previous worktree"},
	} {
		var out bytes.Buffer
		reportSwitchFailure(&out, tc.result)
		if !strings.Contains(out.String(), tc.want) {
			t.Errorf("%s reported as %q, want it to mention %q", tc.result.Error, out.String(), tc.want)
		}
	}
}

func TestReportTargetErrorTellsYouHowToStartOne(t *testing.T) {
	var out bytes.Buffer
	reportTargetError(&out, errNoMarquee)
	if !strings.Contains(out.String(), "marquee -- ") {
		t.Errorf("no-marquee message = %q, want the start command", out.String())
	}
}

func TestPrintStatusMarksTheCurrentWorktree(t *testing.T) {
	target := targetServing("127.0.0.1:3000", "/repo/main", "/repo/feature")
	target.status.Branch = "feature"
	target.status.Dirty = true
	target.status.Worktree.Slug = "feature"
	target.status.Worktree.Path = "/repo/feature"
	target.status.Child.State = "running"

	var out bytes.Buffer
	printStatus(&out, target)
	got := out.String()
	for _, want := range []string{"127.0.0.1:3000", "feature (dirty)", "running", "* feature", "  main"} {
		if !strings.Contains(got, want) {
			t.Errorf("status output %q does not contain %q", got, want)
		}
	}
}
