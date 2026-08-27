// Package discovery reconciles Twitch campaign and inventory data (captured
// passively from the page's own GQL traffic) into the store, applies the game
// filter, and auto-enqueues newly seen drops.
package discovery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/store"
	"github.com/rake-pro/dropwarden/internal/twitch"
)

// Diagnostic is a snapshot of the last discovery pass, for the debug UI.
type Diagnostic struct {
	At              time.Time `json:"at"`
	GQLResponses    int       `json:"gqlResponses"`
	OpsSeen         []string  `json:"opsSeen"`
	HeaderKeys      []string  `json:"headerKeys"`
	CampaignsParsed int       `json:"campaignsParsed"`
	DetailsParsed   int       `json:"detailsParsed"`
	Enqueued        int       `json:"enqueued"`
}

// Discoverer runs a single discovery pass on demand. The jittered scheduling
// loop lives in the watcher engine, which calls RunOnce during Idle/Cooldown.
type Discoverer struct {
	store  *store.Store
	pages  *twitch.Pages
	cfg    *config.Config
	logger zerolog.Logger

	mu   sync.Mutex
	last Diagnostic
}

// LastDiagnostic returns a snapshot of the most recent discovery pass.
func (d *Discoverer) LastDiagnostic() Diagnostic {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.last
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
	headerKeys := d.pages.CapturedHeaderKeys(ctx)
	d.logger.Info().
		Int("gql_responses", captured.RawCount).
		Strs("ops_seen", captured.OpsSeen).
		Int("campaigns_parsed", len(captured.Campaigns)).
		Int("details_parsed", len(captured.Details)).
		Strs("captured_header_keys", headerKeys).
		Msg("discovery: capture diagnostic")
	if len(captured.Campaigns) == 0 && captured.CampaignsRaw != "" {
		sample := captured.CampaignsRaw
		if len(sample) > 700 {
			sample = sample[:700]
		}
		d.logger.Warn().Str("campaigns_body_sample", sample).
			Msg("discovery: campaigns op captured but parsed 0 - response shape sample follows")
	}
	if d.cfg.Logging.CaptureGQLBodies && captured.CampaignsRaw != "" {
		d.dumpDebug("dashboard.json", []byte(captured.CampaignsRaw))
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

	// The dashboard list has no drops or channel ACL; fetch details for the
	// campaigns we care about (matching the game filter, active) so they become
	// watchable. Capped per cycle to stay gentle.
	res.Details += d.fetchDetails(ctx, captured.Campaigns)

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

	d.mu.Lock()
	d.last = Diagnostic{
		At: time.Now(), GQLResponses: captured.RawCount, OpsSeen: captured.OpsSeen,
		HeaderKeys: headerKeys, CampaignsParsed: len(captured.Campaigns),
		DetailsParsed: res.Details, Enqueued: res.Enqueued,
	}
	d.mu.Unlock()

	d.logger.Info().Int("campaigns", res.Campaigns).Int("details", res.Details).
		Int("progress", res.Progress).Int("enqueued", res.Enqueued).Msg("discovery pass complete")
	return res, nil
}

// fetchDetails fetches full campaign details (drops + ACL) for filter-matching
// active campaigns that do not yet have drops, capped at details_per_cycle. It
// returns the number of campaigns whose details were fetched and stored.
func (d *Discoverer) fetchDetails(ctx context.Context, listed []domain.Campaign) int {
	limit := d.cfg.Discovery.DetailsPerCycle
	if limit <= 0 {
		limit = 8
	}
	// Candidates: matching game, active, and not already carrying drops.
	var targets []domain.Campaign
	matched, complete := 0, 0
	for _, c := range listed {
		if c.Status == domain.StatusExpired || !d.matchesFilter(c) {
			continue
		}
		matched++
		// Skip only when we already have COMPLETE details: drops, a game slug
		// (needed for any-channel directory lookups), and the account-connected
		// signal. A campaign missing any of those was left incomplete (e.g. a row
		// written before that field existed), so re-fetch it.
		if existing, err := d.store.GetCampaign(c.ID); err == nil && existing != nil &&
			len(existing.Drops) > 0 && existing.GameSlug != "" && existing.AccountConnected != nil {
			complete++
			continue
		}
		targets = append(targets, c)
		if len(targets) >= limit {
			break
		}
	}
	if len(targets) == 0 {
		if matched > 0 {
			d.logger.Info().Int("matched", matched).Int("already_detailed", complete).
				Msg("discovery: all filter-matching campaigns already have details; nothing to detail-fetch")
		} else {
			d.logger.Info().Bool("any_game", d.cfg.AnyGame).Strs("games", d.cfg.Games).
				Msg("discovery: no active campaigns matched the game filter; nothing to detail-fetch")
		}
		return 0
	}

	login, err := d.pages.FetchLogin(ctx)
	if err != nil {
		d.logger.Warn().Err(err).Msg("discovery: cannot fetch login; skipping campaign details this cycle")
		return 0
	}

	n := 0
	for _, c := range targets {
		full, raw, err := d.pages.FetchCampaignDetails(ctx, login, c.ID)
		if d.cfg.Logging.CaptureGQLBodies && raw != "" {
			d.dumpDebug("details-"+safeName(c.ID)+".json", []byte(raw))
		}
		if err != nil {
			d.logger.Warn().Err(err).Str("campaign", c.ID).Msg("discovery: fetch details failed")
			continue
		}
		if full.ID == "" {
			continue
		}
		if err := d.store.UpsertCampaign(full); err != nil {
			d.logger.Warn().Err(err).Str("campaign", c.ID).Msg("discovery: store details failed")
			continue
		}
		d.logger.Info().Str("campaign", full.Name).Str("game", full.GameName).
			Int("drops", len(full.Drops)).Int("acl", len(full.AllowedChannels)).Msg("discovery: fetched details")
		n++
	}
	return n
}

// reconcileQueue keeps the queue in sync with the current game filter: it prunes
// items whose campaign no longer matches (e.g. after turning off any_game),
// expired, or was claimed, and - when auto-enqueue is on - adds drops from
// matching active campaigns. The queue therefore only ever holds current,
// workable items; claimed drops live in history.
func (d *Discoverer) reconcileQueue(ctx context.Context) (int, error) {
	campaigns, err := d.store.ListCampaigns()
	if err != nil {
		return 0, err
	}
	existing, err := d.store.GetQueue()
	if err != nil {
		return 0, err
	}
	now := time.Now()

	// desired = unclaimed drops of matching, in-window campaigns.
	desired := map[string]domain.Campaign{}
	for _, c := range campaigns {
		if !d.matchesFilter(c) || c.Status == domain.StatusExpired {
			continue
		}
		if !c.EndAt.IsZero() && !c.EndAt.After(now) {
			continue
		}
		for _, drop := range c.Drops {
			if has, _ := d.store.HasClaim(drop.ID); has {
				continue
			}
			desired[drop.ID] = c
		}
	}

	// Prune anything not desired, but never drop the item being watched right now.
	keep := make(map[string]bool, len(desired))
	for id := range desired {
		keep[id] = true
	}
	queued := make(map[string]bool, len(existing))
	for _, it := range existing {
		queued[it.DropID] = true
		if it.Status == domain.ItemActive {
			keep[it.DropID] = true
		}
	}
	if removed, err := d.store.PruneQueue(keep); err != nil {
		return 0, err
	} else if removed > 0 {
		d.logger.Info().Int("removed", removed).Msg("discovery: pruned stale/off-filter queue items")
	}

	if !d.cfg.Queue.AutoEnqueueNew {
		return 0, nil
	}
	pos, err := d.store.NextPosition()
	if err != nil {
		return 0, err
	}
	added := 0
	for id, c := range desired {
		if queued[id] {
			continue
		}
		it := domain.QueueItem{DropID: id, CampaignID: c.ID, Position: pos, Enabled: true, Status: domain.ItemPending}
		if c.RequiresAccountLink() && d.cfg.Claim.SkipAccountLink {
			it.Status = domain.ItemNeedsAction
			it.Reason = "account link required"
		}
		if err := d.store.UpsertQueueItem(it); err != nil {
			return added, err
		}
		queued[id] = true
		pos++
		added++
	}
	return added, nil
}

// dumpDebug writes a raw GraphQL body to <approot>/debug/<name> when the
// capture_gql_bodies flag is on. These files contain campaign JSON only (no auth
// headers), so they are safe to share for diagnosing response shapes.
func (d *Discoverer) dumpDebug(name string, body []byte) {
	dir, err := config.Dir()
	if err != nil {
		return
	}
	ddir := filepath.Join(dir, "debug")
	if err := os.MkdirAll(ddir, 0o700); err != nil {
		return
	}
	path := filepath.Join(ddir, name)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		d.logger.Warn().Err(err).Msg("discovery: write debug dump failed")
		return
	}
	d.logger.Info().Str("file", path).Int("bytes", len(body)).Msg("discovery: wrote debug dump")
}

// safeName sanitizes a string for use as a filename.
func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, s)
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
