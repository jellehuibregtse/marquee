package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

// A session is what a second marquee process — the `status` and `switch`
// subcommands — needs to reach a running one: where it listens, the switch
// token it minted, and the pid that says whether it is still there. Wrapper
// mode writes one at startup and removes it on the way out, beside the pidfile
// and with the same 0600-inside-0700 permissions.
//
// The token on disk grants a local attacker nothing new. It already reaches
// every injected page through the `<marquee-bar token="…">` attribute, so any
// process running as you can read it with a single request to the proxy, and
// such a process can already signal the child directly. The token's actual job
// is refusing a cross-origin request made by a browser, and a file cannot help
// a browser. See docs/security.md, Threats 3 and 7.
type session struct {
	Listen string `json:"listen"`
	Token  string `json:"token"`
	PID    int    `json:"pid"`
}

// marqueeCacheDir is the per-user directory holding both the pidfile and the
// session file. It is created 0700, so the 0600 files inside it are unreadable
// by other users even on a system whose umask is wide open.
func marqueeCacheDir() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "marquee"), nil
}

// listenKey names the artifacts belonging to one listen address. Hashing keeps
// a path separator or a colon in the address out of a file name; eight bytes is
// far more than enough to separate the handful of marquees one user runs.
func listenKey(listen string) string {
	sum := sha256.Sum256([]byte(listen))
	return hex.EncodeToString(sum[:8])
}

func sessionPath(listen string) (string, error) {
	dir, err := marqueeCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, listenKey(listen)+".json"), nil
}

func writeSession(path string, s session) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(body, '\n'), 0o600)
}

func removeSession(path string) {
	_ = os.Remove(path)
}

// readSession reads one session file. A file that does not parse, or that is
// missing the fields a client needs, is reported as an error rather than
// returned half-built, so no caller ever dials an empty address.
func readSession(path string) (session, error) {
	// #nosec G304 -- path is built from a sha256 of the listen address under the user cache dir, never from HTTP or user input.
	body, err := os.ReadFile(path)
	if err != nil {
		return session{}, err
	}
	var s session
	if err := json.Unmarshal(body, &s); err != nil {
		return session{}, err
	}
	if s.Listen == "" || s.PID <= 0 {
		return session{}, errors.New("session file is missing a listen address or pid")
	}
	return s, nil
}

// liveSessions returns the sessions whose marquee is still running, sorted by
// listen address so the CLI's "which one did you mean" message is stable. A
// session whose process is gone is deleted on the way past: the file is only a
// hint, and a stale one holds a token that authenticates nothing, since the
// token is minted per process.
func liveSessions(dir string) ([]session, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	var live []session
	for _, path := range paths {
		s, err := readSession(path)
		if err != nil || !processAlive(s.PID) {
			removeSession(path)
			continue
		}
		live = append(live, s)
	}
	sort.Slice(live, func(i, j int) bool { return live[i].Listen < live[j].Listen })
	return live, nil
}

// processAlive probes a pid with signal 0 — an existence check that sends no
// real signal. EPERM counts as alive: the process is there, it just is not ours
// to signal.
func processAlive(pid int) bool {
	if pid <= 1 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
