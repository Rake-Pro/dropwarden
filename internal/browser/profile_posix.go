//go:build !windows

package browser

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/rs/zerolog"
)

// killLockOwner is the POSIX counterpart to the Windows kill-on-close job
// object: it reaps a Chrome left holding the watcher profile from a previous run
// that ended without cleaning up (a crash, or the app being force-killed).
//
// Chrome's SingletonLock is a symlink whose target is "hostname-pid". The
// watcher profile dir is dropwarden's exclusively, so a lock still present at
// launch is an orphan - and Chrome's singleton would forward our launch to it
// and exit, leaving us without a DevTools port (the same wedge that stranded the
// Windows build). If the named pid is a live process on THIS host, kill its
// process group before the stale symlink is removed.
func killLockOwner(dir string, logger zerolog.Logger) {
	target, err := os.Readlink(filepath.Join(dir, "SingletonLock"))
	if err != nil {
		return // no lock, or not a symlink: nothing to reap
	}
	// The pid follows the LAST '-'; hostnames may themselves contain hyphens.
	i := strings.LastIndex(target, "-")
	if i < 0 {
		return
	}
	host, pidStr := target[:i], target[i+1:]
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 1 {
		return
	}
	if self, herr := os.Hostname(); herr == nil && host != "" && host != self {
		return // lock belongs to another machine (a shared profile): leave it
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	// Signal 0 probes liveness without delivering a signal.
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return // stale pid: the plain lock removal will clear the symlink
	}
	logger.Warn().Int("pid", pid).Msg("killing orphaned chrome that still holds the watcher profile lock")
	if pgid, perr := syscall.Getpgid(pid); perr == nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return
	}
	_ = proc.Kill()
}
