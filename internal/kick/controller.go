// Package kick turns a generic browser.Session into Kick-aware operations:
// discovering drops campaigns, reading playback health, driving the player,
// and claiming rewards. It is the Kick counterpart of internal/twitch and
// implements the same platform interfaces; every selector is verify-live (see
// selectors.go and docs/NOTES-kick.md).
package kick

import (
	"context"

	"github.com/rake-pro/dropwarden/internal/browser"
	"github.com/rake-pro/dropwarden/internal/platform"
)

// controller is the concrete platform.Controller for Kick.
type controller struct {
	sess browser.Session
}

// NewController wraps a session in the Kick-aware controller.
func NewController(sess browser.Session) platform.Controller {
	return &controller{sess: sess}
}

func (c *controller) Session() browser.Session { return c.sess }

func (c *controller) Probe(ctx context.Context) (platform.Health, error) {
	var h platform.Health
	if err := c.sess.Eval(ctx, jsHealthProbe, &h); err != nil {
		return platform.Health{}, err
	}
	return h, nil
}

func (c *controller) ApplyPlayerPolicy(ctx context.Context, p platform.PlayerPolicy) error {
	// Seed for future documents and apply to the current one. Kick has no
	// known quality/volume localStorage keys (verify live), so LowestQuality
	// is currently a no-op.
	if err := c.sess.SeedOnNewDocument(ctx, jsSeedPlayer(p.Volume)); err != nil {
		return err
	}
	_ = c.sess.Eval(ctx, jsSeedPlayer(p.Volume), nil)
	return nil
}

func (c *controller) ReassertVolume(ctx context.Context, p platform.PlayerPolicy) error {
	return c.sess.Eval(ctx, jsReassertVolume(p.Volume), nil)
}

func (c *controller) ClickPlay(ctx context.Context) error {
	_, err := c.sess.Click(ctx, selPlayButton)
	return err
}

// ToggleDropsPanel is a no-op on Kick: progress lives on the page itself and
// the inventory, with no known equivalent of Twitch's chat drops panel.
func (c *controller) ToggleDropsPanel(ctx context.Context) error { return nil }

// CancelRaid is a no-op: Kick hosting/raid behavior is unverified; the
// engine's navigation watchdog still catches a redirected channel.
func (c *controller) CancelRaid(ctx context.Context) (bool, error) { return false, nil }

// ClaimInInventory clicks a claim affordance on the loaded inventory page. The
// configured selector/text are Twitch markup (claim.inventory_button_*), so
// Kick keeps its own verify-live selector and ignores them.
func (c *controller) ClaimInInventory(ctx context.Context, _, _ string) (bool, error) {
	return c.sess.Click(ctx, selClaimButton)
}
