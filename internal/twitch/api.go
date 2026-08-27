// Package twitch turns a generic browser.Session into Twitch-aware operations:
// discovering campaigns, reading playback health, driving the player, and
// claiming drops. All DOM selectors and GQL operation names live under this
// package so a Twitch redesign is contained here.
package twitch

import (
	"context"

	"github.com/rake-pro/dropwarden/internal/browser"
)

// Health is the single snapshot the playback watchdog reads each tick, produced
// by one Eval call (one round trip) against the channel page.
type Health struct {
	Pathname       string  `json:"pathname"`
	ReadyState     int     `json:"readyState"`
	Paused         bool    `json:"paused"`
	Ended          bool    `json:"ended"`
	CurrentTime    float64 `json:"currentTime"`
	Volume         float64 `json:"volume"`
	Muted          bool    `json:"muted"`
	OfflineGate    bool    `json:"offlineGate"`
	PlayerError    bool    `json:"playerError"`
	RaidBanner     bool    `json:"raidBanner"`
	RaidTarget     string  `json:"raidTarget"`
	DropsIndicator bool    `json:"dropsIndicator"`
}

// Playing reports whether the video is advancing per this snapshot alone
// (stall history is tracked by the watchdog).
func (h Health) Playing() bool {
	return h.ReadyState >= 3 && !h.Paused && !h.Ended
}

// PlayerPolicy is applied before navigation and reasserted periodically. Volume
// must be in (0,1]; a muted (0) player is paused when backgrounded on Chrome 145+.
type PlayerPolicy struct {
	Volume        float64
	LowestQuality bool
}

// Controller is the watcher's Twitch-aware view of the browser. It wraps a
// browser.Session and applies Twitch selectors/JS. The watcher depends on this
// interface so the state machine is testable with a fake.
type Controller interface {
	// Session exposes the underlying generic session for navigation/events.
	Session() browser.Session

	// Probe returns a fresh playback-health snapshot for the current channel page.
	Probe(ctx context.Context) (Health, error)

	// ApplyPlayerPolicy seeds volume/quality (localStorage) so they take effect
	// on the next navigation.
	ApplyPlayerPolicy(ctx context.Context, p PlayerPolicy) error
	// ReassertVolume forces the live video element back to the policy volume.
	ReassertVolume(ctx context.Context, p PlayerPolicy) error

	// ClickPlay clicks the player's play control to recover a paused video.
	ClickPlay(ctx context.Context) error
	// ToggleDropsPanel opens then closes the chat drops panel to force a requery.
	ToggleDropsPanel(ctx context.Context) error
	// CancelRaid clicks the raid banner's cancel control; returns false if absent.
	CancelRaid(ctx context.Context) (bool, error)

	// ClaimVisible clicks an on-screen Claim affordance (toast/drops panel);
	// returns false if none is present.
	ClaimVisible(ctx context.Context) (bool, error)
	// ClaimInInventory clicks the Claim button matching claimID on the already
	// loaded inventory page; returns false if not found.
	ClaimInInventory(ctx context.Context, claimID string) (bool, error)
}
