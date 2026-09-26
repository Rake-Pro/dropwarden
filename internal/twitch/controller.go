package twitch

import (
	"context"
	"fmt"
	"strings"

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
	// A content-classification (mature) gate replaces the player until it is
	// acknowledged - accept it first; the player then loads and the next
	// validation tick sees real playback. ClickPlay is invoked every
	// validation iteration while the video is not playing, so this needs no
	// separate engine hook.
	if clicked, _ := c.sess.Click(ctx, selContentGateAccept); clicked {
		return nil
	}
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

// ClaimInInventory clicks the claim button on the already-loaded inventory
// page: the first element matching selector whose label text equals text
// (case-insensitive; both from config claim.inventory_button_*). It returns
// false with no error when selector is empty or nothing matches. The match is
// done in page JS (tag the element), the click is the session's real CDP mouse
// dispatch on the tagged element.
func (c *controller) ClaimInInventory(ctx context.Context, selector, text string) (bool, error) {
	if strings.TrimSpace(selector) == "" {
		return false, nil
	}
	var res struct {
		Found   bool   `json:"found"`
		Matches int    `json:"matches"`
		Error   string `json:"error"`
	}
	if err := c.sess.Eval(ctx, jsMarkClaimButton(selector, text), &res); err != nil {
		return false, fmt.Errorf("twitch: find claim button: %w", err)
	}
	if res.Error != "" {
		return false, fmt.Errorf("twitch: find claim button (selector %q): %s", selector, res.Error)
	}
	if !res.Found {
		return false, nil
	}
	return c.sess.Click(ctx, selMarkedClaim)
}
