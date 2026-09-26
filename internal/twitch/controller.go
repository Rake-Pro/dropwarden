package twitch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

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

// Claim button wait on the inventory page. Live 2026-09-25 a capture 8 s after
// load held no claim button at all (the claimable card had not rendered), so
// the fallback polls, then pages in more cards via "Load More", before giving
// up. Vars so tests can shrink them.
var (
	claimPollWindow = 30 * time.Second // first poll after the page settled
	claimPollEvery  = time.Second
	loadMoreRounds  = 5
	loadMoreGap     = 2 * time.Second // poll window after each Load More round
)

// ClaimInInventory clicks the claim button on the already-loaded inventory
// page (selector and text from config claim.inventory_button_*), matched by
// jsMarkClaimButton's tiers: label equals text, then label or aria-label
// contains it, then any button labelled "claim". It polls for the button every
// claimPollEvery for claimPollWindow; if none shows, it clicks every visible
// "Load More" button and scrolls to the bottom, up to loadMoreRounds rounds
// loadMoreGap apart, polling again after each. It returns false with no error
// when selector is empty or nothing matched; on a miss candidates holds the
// trimmed labels of up to 12 buttons on the page. The match is done in page JS
// (tag the element), the click is the session's real CDP mouse dispatch on
// the tagged element.
func (c *controller) ClaimInInventory(ctx context.Context, selector, text string) (bool, []string, error) {
	if strings.TrimSpace(selector) == "" {
		return false, nil, nil
	}
	res, err := c.pollClaimButton(ctx, selector, text, claimPollWindow)
	for round := 1; err == nil && !res.Found && round <= loadMoreRounds && ctx.Err() == nil; round++ {
		var n int
		if lerr := c.sess.Eval(ctx, jsLoadMore, &n); lerr != nil || n == 0 {
			break
		}
		log.Info().Int("round", round).Int("buttons", n).Msg("twitch: no claim button yet; clicked Load More")
		res, err = c.pollClaimButton(ctx, selector, text, loadMoreGap)
	}
	if err != nil {
		return false, res.Candidates, err
	}
	if !res.Found {
		return false, res.Candidates, nil
	}
	if res.Tier != "exact" {
		log.Info().Str("tier", res.Tier).Str("text", text).Msg("twitch: claim button matched by a looser rule; consider updating claim.inventory_button_*")
	}
	ok, err := c.sess.Click(ctx, selMarkedClaim)
	return ok, nil, err
}

// pollClaimButton runs jsMarkClaimButton every claimPollEvery until it finds
// (and tags) the claim button, reports a page error, or window elapses. It
// always probes at least once.
func (c *controller) pollClaimButton(ctx context.Context, selector, text string, window time.Duration) (claimMarkResult, error) {
	deadline := time.Now().Add(window)
	for {
		var res claimMarkResult
		if err := c.sess.Eval(ctx, jsMarkClaimButton(selector, text), &res); err != nil {
			return res, fmt.Errorf("twitch: find claim button: %w", err)
		}
		if res.Error != "" {
			return res, fmt.Errorf("twitch: find claim button (selector %q): %s", selector, res.Error)
		}
		if res.Found || !time.Now().Before(deadline) {
			return res, nil
		}
		select {
		case <-ctx.Done():
			return res, nil
		case <-time.After(claimPollEvery):
		}
	}
}

// claimMarkResult is jsMarkClaimButton's answer. Tier names which rule
// matched ("exact", "contains", "claim"); Candidates is filled on a miss.
type claimMarkResult struct {
	Found      bool     `json:"found"`
	Matches    int      `json:"matches"`
	Tier       string   `json:"tier"`
	Candidates []string `json:"candidates"`
	Error      string   `json:"error"`
}
