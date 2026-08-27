package twitch

import (
	"context"

	"github.com/rake-pro/dropwarden/internal/browser"
)

// controller is the concrete Controller: a thin Twitch-aware wrapper over a
// generic browser.Session.
type controller struct {
	sess browser.Session
}

// NewController wraps a session in the Twitch-aware controller.
func NewController(sess browser.Session) Controller {
	return &controller{sess: sess}
}

func (c *controller) Session() browser.Session { return c.sess }

// InstallCapture seeds the passive GQL capture shim on a session so subsequent
// navigations buffer the page's own GraphQL responses. Call once after launch,
// before any drop-related navigation.
func InstallCapture(ctx context.Context, sess browser.Session) error {
	return sess.SeedOnNewDocument(ctx, jsCaptureShim)
}

func (c *controller) Probe(ctx context.Context) (Health, error) {
	var h Health
	if err := c.sess.Eval(ctx, jsHealthProbe, &h); err != nil {
		return Health{}, err
	}
	return h, nil
}

func (c *controller) ApplyPlayerPolicy(ctx context.Context, p PlayerPolicy) error {
	// Seed for the next navigation, and apply immediately for the current page.
	if err := c.sess.SeedOnNewDocument(ctx, jsSeedPlayer(p.Volume, p.LowestQuality)); err != nil {
		return err
	}
	_ = c.sess.Eval(ctx, jsSeedPlayer(p.Volume, p.LowestQuality), nil)
	return nil
}

func (c *controller) ReassertVolume(ctx context.Context, p PlayerPolicy) error {
	return c.sess.Eval(ctx, jsReassertVolume(p.Volume), nil)
}

func (c *controller) ClickPlay(ctx context.Context) error {
	_, err := c.sess.Click(ctx, selPlayButton)
	return err
}

// ToggleDropsPanel opens then closes the chat drops panel to nudge the client
// into re-querying drop progress.
func (c *controller) ToggleDropsPanel(ctx context.Context) error {
	if _, err := c.sess.Click(ctx, selDropsChatIcon); err != nil {
		return err
	}
	_, err := c.sess.Click(ctx, selDropsChatIcon)
	return err
}

func (c *controller) CancelRaid(ctx context.Context) (bool, error) {
	return c.sess.Click(ctx, selRaidCancel)
}

func (c *controller) ClaimVisible(ctx context.Context) (bool, error) {
	return c.sess.Click(ctx, selClaimButton)
}

// ClaimInInventory clicks a Claim button on the already-loaded inventory page.
// verify live: matching a specific claimID to its card - claim_id
// (UserID#CampaignID#DropID) is not exposed in the DOM, so for MVP this clicks
// the first visible Claim button, which is correct when only the drop we just
// completed is claimable. The claimID is accepted for future precise matching.
func (c *controller) ClaimInInventory(ctx context.Context, claimID string) (bool, error) {
	_ = claimID
	return c.sess.Click(ctx, selClaimButton)
}
