package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// pidfilePath is where marquee records the child's process-group id for
// a given listen address, so the next start on the same address can
// detect a child that survived a killed marquee.
func pidfilePath(listen string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(listen))
	return filepath.Join(cache, "marquee", hex.EncodeToString(sum[:8])+".pid"), nil
}

func writePidfile(path string, pgid int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(pgid)+"\n"), 0o600)
}

func removePidfile(path string) {
	_ = os.Remove(path)
}

// warnStaleChild inspects a pidfile left behind by a previous run. A
// pidfile whose process group is gone (or whose contents are garbage) is
// removed silently. A still-alive group gets a warning on w and nothing
// else — deliberately no kill, as the warning text explains.
func warnStaleChild(path string, w io.Writer) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	pgid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pgid <= 1 {
		removePidfile(path)
		return
	}
	if !groupAlive(pgid) {
		removePidfile(path)
		return
	}
	_, _ = fmt.Fprintf(w, "marquee: warning: process group %d (recorded in %s) is still running — it looks like a child from a previous marquee that was killed before it could clean up. Not touching it, because the group id may by now belong to an unrelated process; if it is your leftover dev server, stop it with: kill -TERM -%d\n", pgid, path, pgid)
}

func groupAlive(pgid int) bool {
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
