package twitch

import (
	"context"
	"fmt"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
)

// settleDelay is a dwell after load to let the client fire its GQL calls before
// we drain the capture buffer. The drops dashboard hydrates a bit slowly on a
// cold navigation, so this is generous. verify live: tune against real timings.
const settleDelay = 7 * time.Second

// Pages drives the Twitch drop-related pages and returns parsed domain data by
// draining the passive capture buffer. It never issues synthetic GQL.
type Pages struct {
	ctrl Controller
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
