// Package version holds the build-time version stamp. Release builds override
// Version via:
//
//	-ldflags "-X github.com/rake-pro/dropwarden/internal/version.Version=1.2.3"
package version

// Version is the app version ("1.2.3", no leading v). Dev builds keep the
// default; the updater treats a "-dev" suffix as "never auto-update".
var Version = "0.0.0-dev"
