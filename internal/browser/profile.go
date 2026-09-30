package browser

import (
	"encoding/json"
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
	markCleanExit(dir, logger)
	return nil
}

// preferencesFile is Chrome's per-profile JSON settings file under the
// user-data-dir's Default profile.
const preferencesFile = "Default/Preferences"

// markCleanExit rewrites profile.exit_type/exited_cleanly in the Default
// profile's Preferences so Chrome does not open the "Restore pages? Chrome
// didn't shut down correctly" bubble on launch.
//
// Chrome records exit_type=Crashed at startup and only flips it back to Normal
// during an orderly shutdown. Instance.Close sends Browser.close and kills the
// process group in the same breath, and the orphan reap plus any dropwarden
// crash kill Chrome outright, so from Chrome's point of view every dropwarden
// run ends in a crash. A missing file (first launch) is left alone.
func markCleanExit(dir string, logger zerolog.Logger) {
	path := filepath.Join(dir, preferencesFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var prefs map[string]any
	if err := json.Unmarshal(raw, &prefs); err != nil {
		logger.Warn().Err(err).Str("file", path).Msg("could not parse chrome preferences; crash-restore bubble may show")
		return
	}
	profile, _ := prefs["profile"].(map[string]any)
	if profile == nil {
		profile = map[string]any{}
		prefs["profile"] = profile
	}
	if profile["exit_type"] == "Normal" && profile["exited_cleanly"] == true {
		return
	}
	profile["exit_type"] = "Normal"
	profile["exited_cleanly"] = true
	out, err := json.Marshal(prefs)
	if err != nil {
		return
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		logger.Warn().Err(err).Str("file", path).Msg("could not mark chrome profile as cleanly exited")
		return
	}
	logger.Debug().Str("file", path).Msg("marked chrome profile as cleanly exited")
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
