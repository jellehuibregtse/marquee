package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func targetServing(listen string, paths ...string) target {
	var t target
	t.listen = listen
	for _, path := range paths {
		t.status.Worktrees = append(t.status.Worktrees, struct {
			Slug   string `json:"slug"`
			Path   string `json:"path"`
			Branch string `json:"branch"`
		}{Slug: filepath.Base(path), Path: path, Branch: filepath.Base(path)})
	}
	return t
}

// TestPickByWorkingDirectoryChoosesTheServingMarquee is the behaviour that lets
// the subcommands work without --listen on a machine running several marquees:
// the one whose worktree list contains the current directory is the one meant.
func TestPickByWorkingDirectoryChoosesTheServingMarquee(t *testing.T) {
	other := t.TempDir()
	mine := t.TempDir()
	here := filepath.Join(mine, "app", "components")
	if err := mkdirAll(here); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	chdir(t, here)

	got, err := pickByWorkingDirectory([]target{
		targetServing("127.0.0.1:3000", other),
		targetServing("127.0.0.1:4000", mine),
	})
	if err != nil {
		t.Fatalf("pickByWorkingDirectory: %v", err)
	}
	if got.listen != "127.0.0.1:4000" {
		t.Errorf("picked %s, want the marquee serving %s", got.listen, mine)
	}
}

// TestPickByWorkingDirectoryRefusesToGuess covers the case that matters most for
// a command that restarts processes: when nothing identifies which marquee was
// meant, it must fail and name the candidates rather than pick one.
func TestPickByWorkingDirectoryRefusesToGuess(t *testing.T) {
	chdir(t, t.TempDir())
	_, err := pickByWorkingDirectory([]target{
		targetServing("127.0.0.1:3000", t.TempDir()),
		targetServing("127.0.0.1:4000", t.TempDir()),
	})
	if err == nil {
		t.Fatal("pickByWorkingDirectory picked one, want a refusal")
	}
	for _, want := range []string{"127.0.0.1:3000", "127.0.0.1:4000", "--listen"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestServesDirectoryDoesNotMatchASiblingPrefix guards the string comparison:
// /repo/app-old must not count as inside /repo/app.
func TestServesDirectoryDoesNotMatchASiblingPrefix(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "app")
	sibling := filepath.Join(root, "app-old")
	for _, dir := range []string{app, sibling} {
		if err := mkdirAll(dir); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	if servesDirectory(targetServing("127.0.0.1:3000", app), sibling) {
		t.Errorf("%s counted as inside %s", sibling, app)
	}
	if !servesDirectory(targetServing("127.0.0.1:3000", app), app) {
		t.Errorf("%s did not count as inside itself", app)
	}
}
