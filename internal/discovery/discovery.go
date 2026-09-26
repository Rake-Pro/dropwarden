// Package discovery reconciles Twitch campaign and inventory data (captured
// passively from the page's own GQL traffic) into the store, applies the game
// filter, and auto-enqueues newly seen drops.
package discovery

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/store"
	"github.com/rake-pro/dropwarden/internal/twitch"
	"github.com/rake-pro/dropwarden/internal/twitch/gql"
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
		c.Platform = domain.PlatformTwitch
		if err := d.store.UpsertCampaign(c); err != nil {
			return res, err
		}
		res.Campaigns++
	}
	for _, c := range captured.Details {
		c.Platform = domain.PlatformTwitch
		if err := d.store.UpsertCampaign(c); err != nil {
			return res, err
		}
		res.Details++
	}

	// The dashboard list has no drops or channel ACL; fetch details for the
	// campaigns we care about (matching the game filter, active) so they become
	// watchable. Capped per cycle to stay gentle.
	dn, pn := d.fetchDetails(ctx, captured.Campaigns)
	res.Details += dn
	res.Progress += pn

	// Debug diagnostic FIRST (before ReadInventory drains/caches the page): dump
	// the inventory page's RAW GraphQL so the earned/claimed-drops shape (which
	// the app does not yet parse) can be seen and owned-reward detection built
	// from real data. This does the fresh first navigation to the inventory
	// page, so its GraphQL actually fires and is captured; ReadInventory below
	// then reuses the (now cached) page for the progress parse. Only when the
	// GQL-body capture toggle is on; the file lands with the other debug dumps.
	if d.cfg.Logging.CaptureGQLBodies {
		if raw, ierr := d.pages.CaptureInventoryRaw(ctx); ierr != nil {
			d.logger.Warn().Err(ierr).Msg("discovery: inventory GQL capture failed")
		} else {
			d.dumpDebug("inventory-gql.json", []byte(raw))
			d.logger.Info().Int("bytes", len(raw)).Msg("discovery: dumped raw inventory GQL (debug)")
		}
	}

	prog, owned, err := d.pages.ReadInventoryFull(ctx)
	if err != nil {
		d.logger.Warn().Err(err).Msg("discovery: inventory read failed; continuing with campaign data")
	} else {
		for _, p := range prog {
			if err := d.store.SaveProgress(p); err != nil {
				return res, err
			}
			res.Progress++
		}
		// Flag drops whose reward the account already owns AFTER saving inventory
		// progress, so an owned drop that also has in-progress minutes keeps its
		// minutes and just gains the owned flag.
		d.markOwnedDrops(owned)
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

// markOwnedDrops flags every known Twitch drop whose reward the account already
// holds (its benefit name matches the earned/owned inventory) so eligibility
// excludes it before watching. Twitch reports such a drop isClaimed=false yet
// never credits it (the reward is already held, typically from a prior campaign
// instance offering the same cosmetic with fresh drop IDs), so without this the
// watcher grinds a participating channel to exhaustion earning nothing; the
// engine's reactive give-up is only the last-resort backstop. The drop's
// progress row is updated in place, preserving any real minutes. Returns the
// number of drops newly flagged.
func (d *Discoverer) markOwnedDrops(owned map[string]bool) int {
	if len(owned) == 0 {
		return 0
	}
	campaigns, err := d.store.ListCampaigns()
	if err != nil {
		d.logger.Warn().Err(err).Msg("discovery: mark owned: list campaigns failed")
		return 0
	}
	n := 0
	for _, c := range campaigns {
		if c.Platform.OrTwitch() != domain.PlatformTwitch {
			continue
		}
		for _, drop := range c.Drops {
			if !rewardOwned(drop, owned) {
				continue
			}
			p, err := d.store.GetProgress(drop.ID)
			if err != nil {
				continue
			}
			// A drop earned in THIS instance (complete, claimable or claimed)
			// puts its own reward into the earned inventory; that is not a prior
			// instance's copy, so it must not be flagged owned (it would read
			// as "Watch anyway" on a row that only needs a manual claim).
			if earnedHere(p) {
				if p.Owned { // heal a flag set by an older build
					p.Owned = false
					_ = d.store.SaveProgress(*p)
				}
				continue
			}
			if claimed, err := d.store.HasClaim(drop.ID); err == nil && claimed {
				continue
			}
			if p == nil {
				p = &domain.Progress{DropID: drop.ID, RequiredMinutes: drop.RequiredMinutes, Source: domain.SourceInventory}
			} else if p.Owned {
				continue // already flagged
			}
			p.Owned = true
			p.ObservedAt = time.Now()
			if err := d.store.SaveProgress(*p); err != nil {
				d.logger.Warn().Err(err).Str("drop", drop.ID).Msg("discovery: mark owned: save failed")
				continue
			}
			n++
			d.logger.Info().Str("drop", drop.Name).Str("campaign", c.Name).
				Msg("discovery: reward already owned - drop will be skipped before watching")
		}
	}
	if n > 0 {
		d.logger.Info().Int("owned_skipped", n).Msg("discovery: flagged already-owned rewards")
	}
	return n
}

// earnedHere reports whether a drop's own progress shows it earned in the
// current campaign instance: watch time met, claimable, or claimed.
func earnedHere(p *domain.Progress) bool {
	return p != nil && (p.Complete() || p.Claimable || p.ClaimedAt != nil)
}

// rewardOwned reports whether a drop's reward is already held: any of its
// benefit names matches the owned set by normalized name. Reward IDs are
// campaign-instance-scoped, so the name is the only stable cross-instance key
// (a new campaign offering the same cosmetic mints fresh IDs).
func rewardOwned(drop domain.Drop, owned map[string]bool) bool {
	for _, b := range drop.Benefits {
		if owned[gql.NormalizeRewardName(b.Name)] {
			return true
		}
	}
	return false
}

// fetchDetails fetches full campaign details (drops + ACL) for filter-matching
// active campaigns that do not yet have drops, capped at details_per_cycle. It
// returns the number of campaigns whose details were fetched and stored, and
// the number of per-drop progress rows saved alongside them (including
// already-claimed drops, so the next eligibility check excludes them).
// claimStateSynced reports whether we have captured per-drop claim/progress
// state for a campaign: true once at least one of its drops has a progress row
// (the details fetch records one per drop that carries a self field). A campaign
// detailed by an older build - whose details query never requested self{isClaimed}
// - has none, so it is treated as unsynced and re-detailed once to heal.
func (d *Discoverer) claimStateSynced(c *domain.Campaign) bool {
	for _, drop := range c.Drops {
		if p, err := d.store.GetProgress(drop.ID); err == nil && p != nil {
			return true
		}
	}
	return false
}

func (d *Discoverer) fetchDetails(ctx context.Context, listed []domain.Campaign) (details, progress int) {
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
		// (needed for any-channel directory lookups), the account-connected
		// signal, AND at least one drop's claim/progress state recorded. The
		// last check is what heals a store detailed by an older build whose
		// details query did not carry self{isClaimed}: such a campaign has no
		// progress rows, so it is re-detailed once to capture claim state (the
		// details fetch now writes a progress row for every drop with a self
		// field, so after one re-fetch it is synced and skipped thereafter).
		// Without this, an already-detailed campaign of claimed drops would be
		// selected forever, earning nothing (live 2026-08-31).
		if existing, err := d.store.GetCampaign(c.ID); err == nil && existing != nil &&
			len(existing.Drops) > 0 && existing.GameSlug != "" && existing.AccountConnected != nil &&
			d.claimStateSynced(existing) {
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
		return 0, 0
	}

	login, err := d.pages.FetchLogin(ctx)
	if err != nil {
		d.logger.Warn().Err(err).Msg("discovery: cannot fetch login; skipping campaign details this cycle")
		return 0, 0
	}

	n, pn := 0, 0
	for _, c := range targets {
		full, prog, raw, err := d.pages.FetchCampaignDetails(ctx, login, c.ID)
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
		// A drop with a "self" field in the details response is either
		// in-progress or fully claimed; either way, save it so a claimed drop
		// (which the inventory/dashboard views omit once the whole campaign is
		// claimed) is excluded from eligibility on the next reconcile.
		for _, p := range prog {
			if err := d.store.SaveProgress(p); err != nil {
				d.logger.Warn().Err(err).Str("campaign", c.ID).Str("drop", p.DropID).
					Msg("discovery: save details progress failed")
				continue
			}
			pn++
		}
		d.logger.Info().Str("campaign", full.Name).Str("game", full.GameName).
			Int("drops", len(full.Drops)).Int("acl", len(full.AllowedChannels)).Msg("discovery: fetched details")
		n++
	}
	return n, pn
}

// reconcileQueue applies the shared platform-scoped reconciliation for Twitch.
func (d *Discoverer) reconcileQueue(ctx context.Context) (int, error) {
	_ = ctx
	return ReconcileQueue(d.store, d.cfg, domain.PlatformTwitch, d.logger)
}

// ReconcileQueue keeps ONE platform's slice of the shared queue in sync with
// the current game filter: it prunes items whose campaign no longer matches
// (e.g. after turning off any_game), expired, or was claimed, and - when
// auto-enqueue is on - adds drops from matching active campaigns. Other
// platforms' campaigns and queue items are invisible to the call, so parallel
// discoverers never prune or enqueue across platforms. The queue therefore
// only ever holds current, workable items; claimed drops live in history.
func ReconcileQueue(st *store.Store, cfg *config.Config, pf domain.Platform, logger zerolog.Logger) (int, error) {
	campaigns, err := st.ListCampaigns()
	if err != nil {
		return 0, err
	}
	existing, err := st.GetQueue()
	if err != nil {
		return 0, err
	}
	now := time.Now()

	desired := map[string]domain.Campaign{}
	for _, c := range campaigns {
		if c.Platform.OrTwitch() != pf.OrTwitch() {
			continue
		}
		if !MatchesGameFilter(cfg, c) || c.Status == domain.StatusExpired {
			continue
		}
		if !c.EndAt.IsZero() && !c.EndAt.After(now) {
			continue
		}
		for _, drop := range c.Drops {
			if has, _ := st.HasClaim(drop.ID); has {
				continue
			}
			desired[drop.ID] = c
		}
	}

	// Prune anything not desired, but never drop the item being watched right
	// now or another platform's items.
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
		if it.Platform.OrTwitch() != pf.OrTwitch() {
			keep[it.DropID] = true
		}
	}
	if removed, err := st.PruneQueue(keep); err != nil {
		return 0, err
	} else if removed > 0 {
		logger.Info().Int("removed", removed).Str("platform", string(pf.OrTwitch())).
			Msg("discovery: pruned stale/off-filter queue items")
	}

	added := 0
	if cfg.Queue.AutoEnqueueNew {
		if added, err = enqueueNew(st, cfg, desired, queued); err != nil {
			return added, err
		}
	}
	if err := ApplyPinRules(st, pf, logger); err != nil {
		return added, err
	}
	return added, nil
}

// enqueueNew appends every desired drop not yet queued to the bottom of the
// queue, returning how many were added.
func enqueueNew(st *store.Store, cfg *config.Config, desired map[string]domain.Campaign, queued map[string]bool) (int, error) {
	pos, err := st.NextPosition()
	if err != nil {
		return 0, err
	}
	added := 0
	for id, c := range desired {
		if queued[id] {
			continue
		}
		it := domain.QueueItem{DropID: id, CampaignID: c.ID, Platform: c.Platform.OrTwitch(), Position: pos, Enabled: true, Status: domain.ItemPending}
		if c.RequiresAccountLink() && cfg.Claim.SkipAccountLink {
			it.Status = domain.ItemNeedsAction
			it.Reason = "account link required"
		}
		if err := st.UpsertQueueItem(it); err != nil {
			return added, err
		}
		queued[id] = true
		pos++
		added++
	}
	return added, nil
}

// PinKey returns the pin-rule identity of a drop: the normalized game name and
// a reward key built from the drop's normalized benefit names (sorted, joined
// by newlines). Reward and drop IDs are campaign-instance-scoped, so names are
// the only stable cross-instance key (the same rule the owned-reward skip
// uses). A drop with no benefits falls back to its normalized drop name. An
// empty reward key means the drop cannot carry a rule.
func PinKey(c domain.Campaign, d domain.Drop) (game, reward string) {
	game = gql.NormalizeRewardName(c.GameName)
	var names []string
	for _, b := range d.Benefits {
		if n := gql.NormalizeRewardName(b.Name); n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		if n := gql.NormalizeRewardName(d.Name); n != "" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return game, strings.Join(names, "\n")
}

// ApplyPinRules re-pins ONE platform's unpinned queue items whose reward
// matches a stored pin rule (a later campaign instance re-offering a starred
// reward under fresh IDs), then compacts pin ranks 1..n and writes the
// resulting ranks back to the rules so the next re-application lands in the
// same place. Items of other platforms are untouched.
func ApplyPinRules(st *store.Store, pf domain.Platform, logger zerolog.Logger) error {
	rules, err := st.ListPinRules(pf)
	if err != nil {
		return err
	}
	items, err := st.GetQueue()
	if err != nil {
		return err
	}
	if len(rules) == 0 && !pinsNeedCompact(items) {
		return nil
	}
	campaigns, err := st.ListCampaigns()
	if err != nil {
		return err
	}
	cmap := make(map[string]domain.Campaign, len(campaigns))
	for _, c := range campaigns {
		cmap[c.ID] = c
	}
	ruleRank := make(map[[2]string]int, len(rules))
	for _, r := range rules {
		ruleRank[[2]string{r.GameName, r.RewardKey}] = r.Rank
	}
	keys := map[string][2]string{} // drop id -> rule key, for this platform's items
	for _, it := range items {
		if it.Platform.OrTwitch() != pf.OrTwitch() {
			continue
		}
		c, ok := cmap[it.CampaignID]
		if !ok {
			continue
		}
		d, ok := findDrop(c, it.DropID)
		if !ok {
			continue
		}
		game, reward := PinKey(c, d)
		if reward == "" {
			continue
		}
		k := [2]string{game, reward}
		keys[it.DropID] = k
		rank, ok := ruleRank[k]
		if !ok || it.Pin > 0 {
			continue
		}
		if rank <= 0 {
			rank = 1
		}
		if err := st.SetPin(it.DropID, rank); err != nil {
			return err
		}
		logger.Info().Str("drop", d.Name).Str("campaign", c.Name).Str("game", c.GameName).Int("rank", rank).
			Msg("discovery: pin rule re-applied")
	}
	if err := st.CompactPins(); err != nil {
		return err
	}
	return syncPinRuleRanks(st, pf, keys, ruleRank)
}

// syncPinRuleRanks writes each pinned item's compacted rank back to its rule.
func syncPinRuleRanks(st *store.Store, pf domain.Platform, keys map[string][2]string, ruleRank map[[2]string]int) error {
	items, err := st.GetQueue()
	if err != nil {
		return err
	}
	for _, it := range items {
		k, ok := keys[it.DropID]
		if !ok || it.Pin == 0 {
			continue
		}
		if rank, has := ruleRank[k]; !has || rank == it.Pin {
			continue
		}
		if err := st.UpsertPinRule(store.PinRule{Platform: pf, GameName: k[0], RewardKey: k[1], Rank: it.Pin}); err != nil {
			return err
		}
		ruleRank[k] = it.Pin
	}
	return nil
}

// pinsNeedCompact reports whether the queue's pin ranks have gaps or ties.
func pinsNeedCompact(items []domain.QueueItem) bool {
	var ranks []int
	for _, it := range items {
		if it.Pin > 0 {
			ranks = append(ranks, it.Pin)
		}
	}
	sort.Ints(ranks)
	for i, r := range ranks {
		if r != i+1 {
			return true
		}
	}
	return false
}

// findDrop looks up a drop by id within a campaign.
func findDrop(c domain.Campaign, dropID string) (domain.Drop, bool) {
	for _, d := range c.Drops {
		if d.ID == dropID {
			return d, true
		}
	}
	return domain.Drop{}, false
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
	return MatchesGameFilter(d.cfg, c)
}

// MatchesGameFilter reports whether a campaign's game is in the configured set
// (or any_game is on). The filter is shared across platforms.
func MatchesGameFilter(cfg *config.Config, c domain.Campaign) bool {
	if cfg.AnyGame {
		return true
	}
	name := strings.ToLower(strings.TrimSpace(c.GameName))
	for _, g := range cfg.Games {
		if strings.ToLower(strings.TrimSpace(g)) == name {
			return true
		}
	}
	return false
}
