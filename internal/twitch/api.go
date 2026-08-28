// Package twitch turns a generic browser.Session into Twitch-aware operations:
// discovering campaigns, reading playback health, driving the player, and
// claiming drops. All DOM selectors and GQL operation names live under this
// package so a Twitch redesign is contained here.
package twitch

import (
	"github.com/rake-pro/dropwarden/internal/platform"
)

// The engine-facing types are canonical in internal/platform (the watcher is
// platform-neutral and kick provides its own implementations). These aliases
// keep twitch-internal code and existing callers unchanged.
type (
	Health       = platform.Health
	PlayerPolicy = platform.PlayerPolicy
	Controller   = platform.Controller
)
