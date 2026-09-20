package kick

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/platform"
)

// settleDelay lets Kick's SPA hydrate (and any Cloudflare interstitial clear)
// before the page is read. verify live: tune against real timings; Cloudflare
// challenges can take longer than app hydration.
const settleDelay = 8 * time.Second

// Pages drives Kick's drop-related pages. The Kick counterpart of twitch.Pages
// and the engine's Pager implementation.
type Pages struct {
	ctrl platform.Controller
}

// NewPages builds a Pages driver over a Controller.
func NewPages(ctrl platform.Controller) *Pages {
	return &Pages{ctrl: ctrl}
}

// OpenChannel navigates to a channel page after seeding the player policy.
func (p *Pages) OpenChannel(ctx context.Context, login string, policy platform.PlayerPolicy) error {
	if err := p.ctrl.ApplyPlayerPolicy(ctx, policy); err != nil {
		return fmt.Errorf("kick: seed player: %w", err)
	}
	if err := p.ctrl.Session().Navigate(ctx, URLBase+"/"+login); err != nil {
		return fmt.Errorf("kick: open channel %s: %w", login, err)
	}
	return nil
}

// ReadInventory loads the inventory page. Parsing the progress DOM is pending
// live shape discovery: for now it dumps the page's text for offline analysis
// (see Discoverer.dumpDebug) and returns nothing, so progress stays unknown
// rather than wrong. verify live.
func (p *Pages) ReadInventory(ctx context.Context) ([]domain.Progress, error) {
	if err := p.ctrl.Session().Navigate(ctx, URLInventory); err != nil {
		return nil, fmt.Errorf("kick: open inventory: %w", err)
	}
	p.settle(ctx)
	return nil, nil
}

// InventoryText returns the inventory page's rendered text (for debug dumps
// while the progress DOM shape is being discovered).
func (p *Pages) InventoryText(ctx context.Context) string {
	var txt string
	_ = p.ctrl.Session().Eval(ctx, `(() => (document.body && document.body.innerText || '').slice(0, 20000))()`, &txt)
	return txt
}

// InventoryRawHTML returns the inventory page's full rendered HTML (Kick renders
// drop progress into the DOM), for a debug dump while the shape is discovered.
func (p *Pages) InventoryRawHTML(ctx context.Context) string {
	var html string
	_ = p.ctrl.Session().Eval(ctx, jsInventoryRawHTML, &html)
	return html
}

// SeedDropsCapture installs the passive fetch/XHR capture shim (debug-only) so
// the drops/inventory/channel pages loaded afterward record their drops-related
// network traffic. Seed BEFORE navigating; idempotent per session.
func (p *Pages) SeedDropsCapture(ctx context.Context) error {
	return p.ctrl.Session().SeedOnNewDocument(ctx, jsSeedNetCapture)
}

// DrainDropsCapture returns the buffered drops-related network records (JSON)
// captured since the shim was seeded, and clears the buffer. Empty when the
// shim was never seeded or nothing matched.
func (p *Pages) DrainDropsCapture(ctx context.Context) (string, error) {
	var raw string
	if err := p.ctrl.Session().Eval(ctx, jsDrainNetCapture, &raw); err != nil {
		return "", fmt.Errorf("kick: drain drops capture: %w", err)
	}
	return raw, nil
}

// DrainHeartbeat has no Kick capture behind it yet; nothing is reported.
func (p *Pages) DrainHeartbeat(context.Context) (domain.Heartbeat, error) {
	return domain.Heartbeat{}, nil
}

// LiveChannels is unsupported on Kick: the engine falls back to treating every
// allowlisted channel as live and lets playback validation sort it out.
func (p *Pages) LiveChannels(context.Context, []string) ([]domain.Channel, error) {
	return nil, errors.ErrUnsupported
}

// DirectoryChannels returns live channel logins for a category slug.
func (p *Pages) DirectoryChannels(ctx context.Context, gameSlug string) ([]string, error) {
	if gameSlug == "" {
		return nil, fmt.Errorf("kick: empty category slug")
	}
	if err := p.ctrl.Session().Navigate(ctx, fmt.Sprintf(URLDirectoryTmpl, gameSlug)); err != nil {
		return nil, fmt.Errorf("kick: open category %s: %w", gameSlug, err)
	}
	p.settle(ctx)
	var logins []string
	if err := p.ctrl.Session().Eval(ctx, jsDirectoryChannels, &logins); err != nil {
		return nil, fmt.Errorf("kick: scrape category: %w", err)
	}
	return logins, nil
}

// FetchCurrentDrop has no Kick equivalent (no known per-session drop API).
func (p *Pages) FetchCurrentDrop(ctx context.Context) (*domain.Progress, error) {
	return nil, nil
}

// FetchInventoryProgress has no known Kick endpoint yet; progress arrives via
// inventory-page parsing once its DOM shape is confirmed live.
func (p *Pages) FetchInventoryProgress(ctx context.Context) ([]domain.Progress, error) {
	return nil, nil
}

// DrainProgress: Kick has no passive capture shim yet; the stream page's
// on-screen progress widget is the future source (verify live).
func (p *Pages) DrainProgress(ctx context.Context) ([]domain.Progress, error) {
	return nil, nil
}

func (p *Pages) settle(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(settleDelay):
	}
}
