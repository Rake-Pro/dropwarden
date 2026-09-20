package twitch

import (
	"context"
	"fmt"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/twitch/gql"
)

// settleDelay is a dwell after load to let the client fire its GQL calls before
// we drain the capture buffer. The drops dashboard hydrates a bit slowly on a
// cold navigation, so this is generous. verify live: tune against real timings.
const settleDelay = 7 * time.Second

// Pages drives the Twitch drop-related pages and returns parsed domain data by
// draining the passive capture buffer. It never issues synthetic GQL.
type Pages struct {
	ctrl  Controller
	login string // cached logged-in user login, for detail queries
}

// NewPages builds a Pages driver over a Controller.
func NewPages(ctrl Controller) *Pages {
	return &Pages{ctrl: ctrl}
}

// DiscoverCampaigns loads the campaigns page and returns whatever campaign list
// and details the client fetched. The caller merges these into the store.
func (p *Pages) DiscoverCampaigns(ctx context.Context) (CaptureResult, error) {
	sess := p.ctrl.Session()
	if err := sess.Navigate(ctx, URLCampaigns); err != nil {
		return CaptureResult{}, fmt.Errorf("twitch: open campaigns: %w", err)
	}
	p.settle(ctx)
	return Drain(ctx, sess)
}

// DrainProgress drains the passive GQL capture buffer and returns any progress
// entries it held (watcher.Pager's platform-neutral capture hook).
func (p *Pages) DrainProgress(ctx context.Context) ([]domain.Progress, error) {
	res, err := Drain(ctx, p.ctrl.Session())
	if err != nil {
		return nil, err
	}
	return res.Progress, nil
}

// DrainHeartbeat drains the player's spade heartbeat and WebSocket drop-event
// capture (installed by the capture shim) and parses it.
func (p *Pages) DrainHeartbeat(ctx context.Context) (domain.Heartbeat, error) {
	var raw string
	if err := p.ctrl.Session().Eval(ctx, jsDrainHeartbeat, &raw); err != nil {
		return domain.Heartbeat{}, fmt.Errorf("twitch: drain heartbeat: %w", err)
	}
	return gql.ParseHeartbeat([]byte(raw))
}

// ReadInventory loads the inventory page and returns server-confirmed progress.
func (p *Pages) ReadInventory(ctx context.Context) ([]domain.Progress, error) {
	sess := p.ctrl.Session()
	if err := sess.Navigate(ctx, URLInventory); err != nil {
		return nil, fmt.Errorf("twitch: open inventory: %w", err)
	}
	p.settle(ctx)
	res, err := Drain(ctx, sess)
	if err != nil {
		return nil, err
	}
	return res.Progress, nil
}

// ReadInventoryFull loads the inventory page and returns both server-confirmed
// progress and the set of rewards the account already owns (normalized names),
// drained from the SAME capture (the drain clears the buffer, so the two cannot
// be read by separate calls). Discovery uses the owned set to skip drops whose
// reward the account already holds - which Twitch never re-credits.
func (p *Pages) ReadInventoryFull(ctx context.Context) ([]domain.Progress, map[string]bool, error) {
	sess := p.ctrl.Session()
	if err := sess.Navigate(ctx, URLInventory); err != nil {
		return nil, nil, fmt.Errorf("twitch: open inventory: %w", err)
	}
	p.settle(ctx)
	res, err := Drain(ctx, sess)
	if err != nil {
		return nil, nil, err
	}
	return res.Progress, res.OwnedRewards, nil
}

// OpenChannel navigates to a channel page after seeding player policy so volume
// and quality are correct on load.
func (p *Pages) OpenChannel(ctx context.Context, login string, policy PlayerPolicy) error {
	if err := p.ctrl.ApplyPlayerPolicy(ctx, policy); err != nil {
		return fmt.Errorf("twitch: seed player: %w", err)
	}
	sess := p.ctrl.Session()
	if err := sess.Navigate(ctx, URLBase+"/"+login); err != nil {
		return fmt.Errorf("twitch: open channel %s: %w", login, err)
	}
	return nil
}

// DirectoryChannels returns live channel logins for a game category. Used to
// find a channel to watch for any-channel (empty ACL) campaigns.
func (p *Pages) DirectoryChannels(ctx context.Context, gameSlug string) ([]string, error) {
	if gameSlug == "" {
		return nil, fmt.Errorf("twitch: empty game slug")
	}
	sess := p.ctrl.Session()
	url := fmt.Sprintf(URLDirectoryTmpl, gameSlug)
	if err := sess.Navigate(ctx, url); err != nil {
		return nil, fmt.Errorf("twitch: open directory %s: %w", gameSlug, err)
	}
	p.settle(ctx)
	var logins []string
	if err := sess.Eval(ctx, jsDirectoryChannels, &logins); err != nil {
		return nil, fmt.Errorf("twitch: scrape directory: %w", err)
	}
	return logins, nil
}

func (p *Pages) settle(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(settleDelay):
	}
}

// CaptureInventoryRaw navigates to the drops inventory page and returns the RAW
// captured GQL buffer (every operation the page fired, verbatim) without
// parsing. It is a debug-only diagnostic: the inventory page carries the
// earned/claimed-drops shape the app does not yet parse, so dumping it lets
// owned-reward detection be built from real data instead of a guessed schema.
func (p *Pages) CaptureInventoryRaw(ctx context.Context) (string, error) {
	sess := p.ctrl.Session()
	if err := sess.Navigate(ctx, URLInventory); err != nil {
		return "", fmt.Errorf("twitch: open inventory: %w", err)
	}
	p.settle(ctx)
	var raw string
	if err := sess.Eval(ctx, jsDrainCapture, &raw); err != nil {
		return "", fmt.Errorf("twitch: drain inventory capture: %w", err)
	}
	return raw, nil
}
