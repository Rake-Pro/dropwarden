//go:build windows

package browser

import "github.com/rs/zerolog"

// killLockOwner is a no-op on Windows: the kill-on-close job object
// (launch_windows.go) means dropwarden's Chrome never outlives the app, so a
// lock present at launch belongs to no live process and the plain removal in
// clearStaleLock clears it. (POSIX has no such OS guarantee, so it reaps the
// lock's owner instead - see profile_posix.go.)
func killLockOwner(dir string, logger zerolog.Logger) {}
