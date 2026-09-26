// Package discovery reconciles Twitch campaign and inventory data (captured
// passively from the page's own GQL traffic) into the store, applies the game
// filter, and auto-enqueues newly seen drops.
package discovery

import (
	"context"
	"errors"
	"fmt"
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
	logger zerolog.Logger

	// DebugDir is where capture_gql_bodies dumps land: the profile's own debug
	// dir (config.ProfilePaths.Debug). Empty falls back to <approot>/debug.
	DebugDir string

	// cfgMu guards cfg: the settings save path swaps it (SetConfig) while a
	// pass may be running on the engine goroutine.
	cfgMu sync.Mutex
	cfg   *config.Config

	mu   sync.Mutex
	last Diagnostic
}

// SetConfig swaps in a newer config (games filter, any_game, capture toggle,
// details_per_cycle) for the next pass. Called from the settings reload path.
func (d *Discoverer) SetConfig(cfg *config.Config) {
	if cfg == nil {
		return
	}
	d.cfgMu.Lock()
	d.cfg = cfg
	d.cfgMu.Unlock()
}

// config returns the current config under the lock.
func (d *Discoverer) config() *config.Config {
	d.cfgMu.Lock()
	defer d.cfgMu.Unlock()
	return d.cfg
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
	cfg := d.config()

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
	if cfg.Logging.CaptureGQLBodies && captured.CampaignsRaw != "" {
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
	dn, pn, err := d.fetchDetails(ctx, cfg, captured.Campaigns)
	if err != nil {
		return res, err
	}
	res.Details += dn
	res.Progress += pn

	// Debug diagnostic FIRST (before ReadInventory drains/caches the page): dump
	// the inventory page's RAW GraphQL so the earned/claimed-drops shape (which
	// the app does not yet parse) can be seen and owned-reward detection built
	// from real data. This does the fresh first navigation to the inventory
	// page, so its GraphQL actually fires and is captured; ReadInventory below
	// then reuses the (now cached) page for the progress parse. Only when the
	// GQL-body capture toggle is on; the file lands with the other debug dumps.
	if cfg.Logging.CaptureGQLBodies {
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

	// Complete-but-unclaimed queue rows whose campaign already left the
	// in-progress inventory (Twitch drops a campaign from that list once it is
	// claimed or finished) never see isClaimed there, and fetchDetails skips
	// already-detailed campaigns: re-detail them so self{isClaimed} lands.
	rn, rp, err := d.redetailUnclaimed(ctx, cfg)
	if err != nil {
		return res, err
	}
	res.Details += rn
	res.Progress += rp

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
				changed := false
				if p.Owned { // heal a flag set by an older build
					p.Owned = false
					changed = true
				}
				// Complete here AND the account holds the reward: nothing is
				// left to do either way (claimed by hand, or a copy is already
				// held), so book it claimed; the engine's reconcile then records
				// it as detected and clears the needs_action row.
				if p.Complete() && p.ClaimedAt == nil {
					now := time.Now()
					p.ClaimedAt = &now
					p.Claimable = false
					changed = true
					d.logger.Info().Str("drop", drop.Name).Str("campaign", c.Name).
						Msg("discovery: complete drop's reward is in the inventory - booking it claimed")
				}
				if changed {
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

func (d *Discoverer) fetchDetails(ctx context.Context, cfg *config.Config, listed []domain.Campaign) (details, progress int, err error) {
	limit := cfg.Discovery.DetailsPerCycle
	if limit <= 0 {
		limit = 8
	}
	// Candidates: matching game, active, and not already carrying drops.
	var targets []domain.Campaign
	matched, complete := 0, 0
	for _, c := range listed {
		if c.Status == domain.StatusExpired || !MatchesGameFilter(cfg, c) {
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
			d.logger.Info().Bool("any_game", cfg.AnyGame).Strs("games", cfg.Games).
				Msg("discovery: no active campaigns matched the game filter; nothing to detail-fetch")
		}
		return 0, 0, nil
	}
	n, pn, err := d.detailCampaigns(ctx, cfg, targets)
	return n, pn, err
}

// detailCampaigns fetches and stores full details (drops, ACL, per-drop
// progress) for each campaign. A logged-out session is returned as
// domain.ErrLoggedOut; any other failure skips the campaign.
func (d *Discoverer) detailCampaigns(ctx context.Context, cfg *config.Config, targets []domain.Campaign) (int, int, error) {
	login, err := d.pages.FetchLogin(ctx)
	if err != nil {
		if errors.Is(err, domain.ErrLoggedOut) {
			return 0, 0, fmt.Errorf("discovery: %w", err)
		}
		d.logger.Warn().Err(err).Msg("discovery: cannot fetch login; skipping campaign details this cycle")
		return 0, 0, nil
	}

	n, pn := 0, 0
	for _, c := range targets {
		full, prog, raw, err := d.pages.FetchCampaignDetails(ctx, login, c.ID)
		if cfg.Logging.CaptureGQLBodies && raw != "" {
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
	return n, pn, nil
}

// redetailBudget caps the claim-state re-detail fetches per pass, separate
// from details_per_cycle.
const redetailBudget = 10

// redetailUnclaimed re-fetches details, oldest observation first and capped
// at redetailBudget campaigns, for every campaign with a needs_action or
// active Twitch queue item whose progress row is complete but not claimed.
// The saved self{isClaimed} then flips the row via the engine's reconcile. A
// row already booked claimed is not a target, so a second pass is a no-op.
func (d *Discoverer) redetailUnclaimed(ctx context.Context, cfg *config.Config) (int, int, error) {
	targets := d.unclaimedTargets()
	if len(targets) == 0 {
		return 0, 0, nil
	}
	d.logger.Info().Int("campaigns", len(targets)).Msg("discovery: re-detailing campaigns with complete-but-unclaimed drops")
	return d.detailCampaigns(ctx, cfg, targets)
}

// unclaimedTargets picks the campaigns redetailUnclaimed fetches.
func (d *Discoverer) unclaimedTargets() []domain.Campaign {
	items, err := d.store.GetQueue()
	if err != nil {
		return nil
	}
	oldest := map[string]time.Time{}
	for _, it := range items {
		if it.Platform.OrTwitch() != domain.PlatformTwitch {
			continue
		}
		if it.Status != domain.ItemNeedsAction && it.Status != domain.ItemActive {
			continue
		}
		p, err := d.store.GetProgress(it.DropID)
		if err != nil || p == nil || !p.Complete() || p.ClaimedAt != nil {
			continue
		}
		if at, ok := oldest[it.CampaignID]; !ok || p.ObservedAt.Before(at) {
			oldest[it.CampaignID] = p.ObservedAt
		}
	}
	ids := make([]string, 0, len(oldest))
	for id := range oldest {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if !oldest[ids[i]].Equal(oldest[ids[j]]) {
			return oldest[ids[i]].Before(oldest[ids[j]])
		}
		return ids[i] < ids[j]
	})
	if len(ids) > redetailBudget {
		ids = ids[:redetailBudget]
	}
	out := make([]domain.Campaign, 0, len(ids))
	for _, id := range ids {
		out = append(out, domain.Campaign{ID: id})
	}
	return out
}

// reconcileQueue applies the shared platform-scoped reconciliation for Twitch.
func (d *Discoverer) reconcileQueue(ctx context.Context) (int, error) {
	_ = ctx
	return ReconcileQueue(d.store, d.config(), domain.PlatformTwitch, d.logger)
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

// dumpDebug writes a raw GraphQL body to the profile's debug dir when the
// capture_gql_bodies flag is on. These files contain campaign JSON only (no auth
// headers), so they are safe to share for diagnosing response shapes.
func (d *Discoverer) dumpDebug(name string, body []byte) {
	ddir := DebugDirOr(d.DebugDir)
	if ddir == "" {
		return
	}
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

// DebugDirOr returns dir, or <approot>/debug when dir is empty (a runtime
// built without a profile debug dir). "" when neither resolves.
func DebugDirOr(dir string) string {
	if dir != "" {
		return dir
	}
	root, err := config.Dir()
	if err != nil {
		return ""
	}
	return filepath.Join(root, "debug")
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
