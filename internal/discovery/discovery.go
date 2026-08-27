// Package discovery reconciles Twitch campaign and inventory data (captured
// passively from the page's own GQL traffic) into the store, applies the game
// filter, and auto-enqueues newly seen drops.
package discovery

import (
	"context"
	"strings"

	"github.com/rs/zerolog"

	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/store"
	"github.com/rake-pro/dropwarden/internal/twitch"
)

// Discoverer runs a single discovery pass on demand. The jittered scheduling
// loop lives in the watcher engine, which calls RunOnce during Idle/Cooldown.
type Discoverer struct {
	store  *store.Store
	pages  *twitch.Pages
	cfg    *config.Config
	logger zerolog.Logger
}

// New builds a Discoverer.
func New(st *store.Store, pages *twitch.Pages, cfg *config.Config, logger zerolog.Logger) *Discoverer {
	return &Discoverer{store: st, pages: pages, cfg: cfg, logger: logger}
}

// Result summarizes what a discovery pass changed.
type Result struct {
	Campaigns int
	Details   int
	Progress  int
	Enqueued  int
}

// RunOnce performs one full discovery: read the campaigns page, read the
// inventory page, reconcile into the store, and auto-enqueue matching drops.
func (d *Discoverer) RunOnce(ctx context.Context) (Result, error) {
	var res Result

	captured, err := d.pages.DiscoverCampaigns(ctx)
	if err != nil {
		return res, err
	}
	// Diagnostic: what the passive capture actually saw. If gql_responses is 0
	// the shim did not observe any GraphQL (capture problem); if it is >0 but
	// campaigns is 0, an operation name or response shape needs updating - the
	// ops_seen list shows the real names. See docs/NOTES-twitch.md.
	d.logger.Info().
		Int("gql_responses", captured.RawCount).
		Strs("ops_seen", captured.OpsSeen).
		Int("campaigns_parsed", len(captured.Campaigns)).
		Int("details_parsed", len(captured.Details)).
		Msg("discovery: capture diagnostic")
	if len(captured.Campaigns) == 0 && captured.DebugSample != "" {
		d.logger.Warn().Str("campaigns_body_sample", captured.DebugSample).
			Msg("discovery: campaigns op captured but parsed 0 - response shape sample follows")
	}

	// List entries first (basic fields), then detail entries (full drops + ACL)
	// so details win on conflict.
	for _, c := range captured.Campaigns {
		if err := d.store.UpsertCampaign(c); err != nil {
			return res, err
		}
		res.Campaigns++
	}
	for _, c := range captured.Details {
		if err := d.store.UpsertCampaign(c); err != nil {
			return res, err
		}
		res.Details++
	}

	prog, err := d.pages.ReadInventory(ctx)
	if err != nil {
		d.logger.Warn().Err(err).Msg("discovery: inventory read failed; continuing with campaign data")
	} else {
		for _, p := range prog {
			if err := d.store.SaveProgress(p); err != nil {
				return res, err
			}
			res.Progress++
		}
	}

	enq, err := d.reconcileQueue(ctx)
	if err != nil {
		return res, err
	}
	res.Enqueued = enq

	d.logger.Info().Int("campaigns", res.Campaigns).Int("details", res.Details).
		Int("progress", res.Progress).Int("enqueued", res.Enqueued).Msg("discovery pass complete")
	return res, nil
}

// reconcileQueue adds drops from filtered, active campaigns that are not already
// queued and not already claimed.
func (d *Discoverer) reconcileQueue(ctx context.Context) (int, error) {
	if !d.cfg.Queue.AutoEnqueueNew {
		return 0, nil
	}
	campaigns, err := d.store.ListCampaigns()
	if err != nil {
		return 0, err
	}
	existing, err := d.store.GetQueue()
	if err != nil {
		return 0, err
	}
	queued := make(map[string]bool, len(existing))
	for _, it := range existing {
		queued[it.DropID] = true
	}

	pos, err := d.store.NextPosition()
	if err != nil {
		return 0, err
	}

	added := 0
	for _, c := range campaigns {
		if !d.matchesFilter(c) || c.Status == domain.StatusExpired {
			continue
		}
		for _, drop := range c.Drops {
			if queued[drop.ID] {
				continue
			}
			claimed, err := d.store.HasClaim(drop.ID)
			if err != nil {
				return added, err
			}
			if claimed {
				continue
			}
			it := domain.QueueItem{
				DropID:     drop.ID,
				CampaignID: c.ID,
				Position:   pos,
				Enabled:    true,
				Status:     domain.ItemPending,
			}
			if c.RequiresAccountLink() && d.cfg.Claim.SkipAccountLink {
				it.Status = domain.ItemNeedsAction
				it.Reason = "account link required"
			}
			if err := d.store.UpsertQueueItem(it); err != nil {
				return added, err
			}
			queued[drop.ID] = true
			pos++
			added++
		}
	}
	return added, nil
}

// matchesFilter reports whether a campaign's game is in the configured set (or
// any_game is on).
func (d *Discoverer) matchesFilter(c domain.Campaign) bool {
	if d.cfg.AnyGame {
		return true
	}
	name := strings.ToLower(strings.TrimSpace(c.GameName))
	for _, g := range d.cfg.Games {
		if strings.ToLower(strings.TrimSpace(g)) == name {
			return true
		}
	}
	return false
}
