package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// controlBodyCap bounds what the CLI reads from a response. Both endpoints
// answer with a small JSON object, so anything larger is not marquee and is not
// worth buffering.
const controlBodyCap = 1 << 20

func readCappedBody(resp *http.Response) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, controlBodyCap))
	if err != nil {
		return nil, fmt.Errorf("reading the response: %w", err)
	}
	return body, nil
}

// controlStatus is the client-side shape of GET /__marquee/status. It names
// only the fields the subcommands render; the endpoint's payload is wider, and
// `--json` prints the server's bytes verbatim rather than anything re-encoded
// from here, so a field added there reaches a script without a client release.
type controlStatus struct {
	Branch   string `json:"branch"`
	Dirty    bool   `json:"dirty"`
	Worktree struct {
		Slug   string `json:"slug"`
		Path   string `json:"path"`
		IsMain bool   `json:"isMain"`
	} `json:"worktree"`
	PR *struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		URL    string `json:"url"`
	} `json:"pr"`
	Worktrees []struct {
		Slug   string `json:"slug"`
		Path   string `json:"path"`
		Branch string `json:"branch"`
	} `json:"worktrees"`
	Child struct {
		State string `json:"state"`
	} `json:"child"`
}

// target is a running marquee a subcommand has resolved, together with the
// status it answered with. Token is empty for a marquee that minted none
// (attach mode, or a failed mint), which is what makes `switch` refusable with
// a real reason instead of a bare 403.
type target struct {
	listen string
	token  string
	status controlStatus
	raw    []byte
}

// errNoMarquee is the "nothing to talk to" case, reported the same way whether
// no session file exists or the one that does belongs to a dead process.
var errNoMarquee = errors.New("no marquee is running")

// resolveTarget picks the marquee a subcommand talks to and reads its status in
// one step, because every subcommand needs both and the status call is also
// what proves the recorded address still answers.
//
// An explicit --listen wins, and is dialled even with no session file behind it
// so that `status` works against `marquee attach`, which mints no token and
// writes no session. Otherwise a single running marquee is the answer; with
// several, the one whose worktree list contains the current directory wins,
// which is what makes the command work from inside any worktree of the repo
// without naming a port.
func resolveTarget(listen string) (target, error) {
	dir, err := marqueeCacheDir()
	if err != nil {
		return target{}, err
	}
	sessions, err := liveSessions(dir)
	if err != nil {
		return target{}, err
	}

	if listen != "" {
		for _, s := range sessions {
			if s.Listen == listen {
				return dial(s.Listen, s.Token)
			}
		}
		return dial(listen, "")
	}

	switch len(sessions) {
	case 0:
		return target{}, errNoMarquee
	case 1:
		return dial(sessions[0].Listen, sessions[0].Token)
	}

	var reachable []target
	for _, s := range sessions {
		t, err := dial(s.Listen, s.Token)
		if err != nil {
			continue
		}
		reachable = append(reachable, t)
	}
	return pickByWorkingDirectory(reachable)
}

// pickByWorkingDirectory chooses among several running marquees by asking which
// one is serving the repository the command was run in: the current directory
// lies inside one of its worktrees. Symlinks are resolved on both sides first,
// because a path git reports and a path a shell arrived by differ on macOS,
// where /tmp is a symlink to /private/tmp.
func pickByWorkingDirectory(candidates []target) (target, error) {
	if len(candidates) == 0 {
		return target{}, errNoMarquee
	}
	wd, err := os.Getwd()
	if err != nil {
		return target{}, err
	}

	var matched []target
	for _, t := range candidates {
		if servesDirectory(t, wd) {
			matched = append(matched, t)
		}
	}
	if len(matched) == 1 {
		return matched[0], nil
	}
	if len(matched) > 1 {
		candidates = matched
	}

	addrs := make([]string, 0, len(candidates))
	for _, t := range candidates {
		addrs = append(addrs, t.listen)
	}
	return target{}, fmt.Errorf("several marquees are running (%s) and %s does not pick one out; name it with --listen",
		strings.Join(addrs, ", "), wd)
}

func servesDirectory(t target, dir string) bool {
	dir = resolveSymlinks(dir)
	for _, wt := range t.status.Worktrees {
		path := resolveSymlinks(wt.Path)
		if dir == path || strings.HasPrefix(dir, path+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// resolveSymlinks canonicalises a path so two spellings of one directory
// compare equal, and leaves a path it cannot resolve alone: a worktree that has
// been deleted since marquee listed it should fail to match, not crash the
// lookup. On macOS this is what makes /tmp and /private/tmp the same place.
func resolveSymlinks(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// dial reads GET /__marquee/status from a listen address. A successful read is
// also the liveness proof the session file cannot give: the recorded process
// may be alive while its listener is not yet, or no longer, answering.
func dial(listen, token string) (target, error) {
	url := "http://" + listen + "/__marquee/status"
	resp, err := http.Get(url) // #nosec G107 -- the address comes from marquee's own session file or the operator's --listen, and is loopback in every supported configuration.
	if err != nil {
		return target{}, fmt.Errorf("no marquee answering on %s: %w", listen, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := readCappedBody(resp)
	if err != nil {
		return target{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return target{}, fmt.Errorf("marquee on %s answered %d for its own status", listen, resp.StatusCode)
	}
	var status controlStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		return target{}, fmt.Errorf("%s is answering, but not like marquee: %w", listen, err)
	}
	return target{listen: listen, token: token, status: status, raw: raw}, nil
}

// switchResult is the switch endpoint's response contract, both halves of it:
// the success body and the error body, decoded into one struct so a caller
// reads Error to tell them apart.
type switchResult struct {
	OK       bool   `json:"ok"`
	Slug     string `json:"slug"`
	Path     string `json:"path"`
	Error    string `json:"error"`
	Message  string `json:"message"`
	Reverted bool   `json:"reverted"`
}

// postSwitch drives POST /__marquee/switch with the same two headers the bar
// sends. There is deliberately no client timeout: a switch runs the operator's
// bootstrap hook in the target worktree, which is minutes of work on a real
// project, and the server already bounds it with --hook-timeout and
// --health-timeout.
func postSwitch(t target, slug string, confirm bool) (switchResult, []byte, error) {
	body, err := json.Marshal(map[string]any{"slug": slug, "confirm": confirm})
	if err != nil {
		return switchResult{}, nil, err
	}
	url := "http://" + t.listen + "/__marquee/switch"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return switchResult{}, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// No browser is involved, so Sec-Fetch-Site is absent and the endpoint's
	// same-origin guard falls back to requiring an Origin that matches Host.
	req.Header.Set("Origin", "http://"+t.listen)
	req.Header.Set("X-Marquee-Token", t.token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return switchResult{}, nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := readCappedBody(resp)
	if err != nil {
		return switchResult{}, nil, err
	}
	var result switchResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return switchResult{}, raw, fmt.Errorf("switch answered %d with a body that is not JSON: %s", resp.StatusCode, raw)
	}
	return result, raw, nil
}
