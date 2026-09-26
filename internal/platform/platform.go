// Package platform defines the interfaces a streaming platform driver must
// implement for the watcher engine. The engine is platform-neutral: twitch
// and kick each provide a Controller (page-level actions) and satisfy the
// engine's Pager (navigation-level actions), and any number of engines run in
// parallel, one per platform, over a shared store.
package platform

import (
	"context"

	"github.com/rake-pro/dropwarden/internal/browser"
)

// Health is a playback-health snapshot of the current channel page.
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

	// Visibility is document.visibilityState: a "hidden" tab (backgrounded in
	// its window) can suppress the player from ever starting, the same failure
	// family as a minimized window - captured so validation failures can name it.
	Visibility string `json:"visibility"`
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

// Controller is the watcher's platform-aware view of the browser. It wraps a
// browser.Session and applies that platform's selectors/JS. The watcher
// depends on this interface so the state machine is testable with a fake.
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
	// ToggleDropsPanel nudges the client into re-querying drop progress.
	ToggleDropsPanel(ctx context.Context) error
	// CancelRaid clicks the raid banner's cancel control; returns false if absent.
	CancelRaid(ctx context.Context) (bool, error)

	// ClaimInInventory clicks the claim button on the already-loaded inventory
	// page: the first element matching selector whose label text equals text
	// (config claim.inventory_button_selector / _text). Returns false (no
	// error) when nothing matches.
	ClaimInInventory(ctx context.Context, selector, text string) (bool, error)
}
