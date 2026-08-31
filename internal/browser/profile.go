package browser

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/rs/zerolog"
)

// devToolsPortFile is where Chrome publishes the port it picked for
// --remote-debugging-port=0. Line 1 is the port, line 2 is the websocket path.
const devToolsPortFile = "DevToolsActivePort"

// ensureProfile prepares the dedicated user-data-dir: it creates the directory
// 0700 (it holds Twitch session cookies), drops a stale singleton lock left by a
// crashed run, and removes the previous DevToolsActivePort file so the launch
// path cannot latch onto a dead port.
func ensureProfile(dir string, logger zerolog.Logger) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("browser: create profile dir %s: %w", dir, err)
	}
	clearStaleLock(dir, logger)
	clearStalePortFile(dir, logger)
	return nil
}

// lockNames are the singleton artifacts Chrome writes into a user-data-dir. On
// POSIX SingletonLock is a symlink whose target encodes host and pid; on Windows
// it is a plain file named lockfile.
func lockNames() []string {
	if runtime.GOOS == "windows" {
		return []string{"lockfile"}
	}
	return []string{"SingletonLock"}
}

// clearStaleLock removes a leftover Chrome singleton lock.
//
// The heuristic is deliberately conservative in scope but optimistic in
// judgement: this profile directory belongs exclusively to dropwarden and is
// never shared with a user's everyday Chrome, so a lock still present at launch
// time means the previous run died without cleaning up. Only the exact lock
// names above are ever touched; nothing else in the profile is removed.
//
// Future improvement: read the SingletonLock symlink target (hostname-pid) and
// probe that pid before deleting, so a genuinely live second instance is left
// alone instead of being stomped.
func clearStaleLock(dir string, logger zerolog.Logger) {
	// A lock present at launch means the previous run died without cleaning up.
	// On POSIX that Chrome may still be alive and holding the profile via its
	// singleton, so reap it before removing the stale lock; on Windows the job
	// object already guarantees it is gone (killLockOwner is a no-op there).
	killLockOwner(dir, logger)
	for _, name := range lockNames() {
		path := filepath.Join(dir, name)
		if _, err := os.Lstat(path); err != nil {
			continue
		}
		logger.Warn().Str("lock", path).Msg("removing stale chrome profile lock from a previous run")
		if err := os.Remove(path); err != nil {
			logger.Warn().Err(err).Str("lock", path).Msg("could not remove stale profile lock")
		}
	}
}

// clearStalePortFile removes a DevToolsActivePort file from a previous run.
// Chrome rewrites it on startup, but the poll loop would otherwise read the old
// contents in the window before it does and connect to nothing.
func clearStalePortFile(dir string, logger zerolog.Logger) {
	path := filepath.Join(dir, devToolsPortFile)
	if _, err := os.Lstat(path); err != nil {
		return
	}
	if err := os.Remove(path); err != nil {
		logger.Warn().Err(err).Str("file", path).Msg("could not remove stale devtools port file")
	}
}
