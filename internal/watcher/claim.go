package watcher

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/twitch/gql"
)

// A live run spent 9+ minutes stuck in a single claim click, so every claim
// step is bounded. The two paths have SEPARATE budgets so a slow mutation path
// never eats the DOM fallback's time:
//
//   - claimAPITimeout: claim id lookup (stored row, current drop, inventory,
//     campaign details) plus the mutation and its verification read; also
//     each sweep claim.
//   - claimTimeout: the DOM fallback - open the inventory (~7 s settle), wait
//     up to 30 s for a claim button, up to 5 "Load More" rounds 2 s apart,
//     click, settle and verify.
const (
	claimAPITimeout = 60 * time.Second
	claimTimeout    = 90 * time.Second
)

// URLInventoryClaim is where the user can claim manually when we could not.
const URLInventoryClaim = "https://www.twitch.tv/drops/inventory"

// URLKickInventory is Kick's drops inventory, where a Kick reward the engine
// could not confirm is claimed by hand.
const URLKickInventory = "https://kick.com/drops/inventory"

// sweepMax caps how many drops one claim sweep pass claims.
const sweepMax = 10

// domClaimSettle is how long the DOM fallback waits after clicking before it
// re-reads the inventory, so the page's own claim request can land.
const domClaimSettle = 3 * time.Second

// rawLogMax truncates raw mutation responses in the debug log.
const rawLogMax = 600

// runClaiming makes ONE bounded attempt to claim the completed drop: the
// platform's claim mutation first (verified by re-reading the inventory), then,
// only if that is unsupported or fails, the inventory page's claim button. On
// failure the item is marked needs_action for the user to reconcile manually
// and the engine moves on - no automatic retries (user decision 2026-08-27);
// the claim sweep picks the drop up again after later discovery passes.
func (e *Engine) runClaiming(parent context.Context) (State, string) {
	if e.cur == nil {
		return StateCooldown, "nothing to claim"
	}
	drop := e.cur.drop
	ctx, cancel := context.WithTimeout(parent, claimAPITimeout)
	defer cancel()

	path, detected := "", false
	claimID := e.claimIDFor(ctx, drop.ID, e.cur.campaign.ID)
	if claimID == "" {
		e.deps.Log.Warn().Str("drop", drop.ID).Msg("claim: no claim id known; skipping the mutation")
	} else {
		status, err := e.claimByAPI(ctx, drop.ID, claimID)
		switch {
		case err == nil:
			path, detected = "gql", status == gql.ClaimStatusAlreadyClaimed
		case errors.Is(err, errors.ErrUnsupported):
			e.deps.Log.Debug().Msg("claim: platform has no claim mutation; using the inventory page")
		default:
			e.deps.Log.Warn().Err(err).Str("drop", drop.ID).Str("status", status).Msg("claim: mutation failed; trying the inventory page")
		}
	}
	if path == "" {
		dctx, dcancel := context.WithTimeout(parent, claimTimeout)
		ok := e.claimByDOM(dctx, parent, drop.ID, claimID)
		dcancel()
		if ok {
			path = "dom"
		}
	}

	if path == "" {
		// The reward remains claimable server-side; surface it to the user and
		// move on to the next drop instead of retrying.
		reason, url := "completed - claim it manually in the Twitch inventory", URLInventoryClaim
		if e.deps.Platform == domain.PlatformKick {
			reason, url = "completed - claim it manually in the Kick inventory", URLKickInventory
		}
		_ = e.deps.Store.SetItemStatus(drop.ID, domain.ItemNeedsAction, reason)
		e.deps.Emit.Emit(EvNeedsAction, NeedsActionPayload{DropID: drop.ID, Reason: reason, URL: url})
		e.deps.Log.Warn().Str("drop", drop.ID).Msg("claim: could not confirm; flagged for manual reconcile")
		return StateCooldown, "claim needs manual reconcile"
	}

	e.deps.Log.Info().Str("drop", drop.Name).Str("path", path).Msg("claim: " + path)
	e.recordClaim(drop, e.cur.campaign, detected)

	// Record the claim on the current session too.
	e.endSession(domain.OutcomeClaimed, "claimed "+drop.Name)
	return StateCooldown, "claimed " + drop.Name
}

// reasonDetected is the queue reason for a drop found already claimed.
const reasonDetected = "claimed (detected in inventory)"

// recordClaim books a confirmed claim: the History row (only if none exists,
// so it is idempotent), the progress row's claimed stamp, the queue item's
// status (also draining a needs_action item back to the claimed state
// reconcile then prunes), the UI event and the log. detected marks a claim
// found already made server-side (inventory isClaimed, or the mutation's
// DROP_INSTANCE_ALREADY_CLAIMED): History gets Source "detected" and the queue
// reason says so.
func (e *Engine) recordClaim(drop domain.Drop, campaign domain.Campaign, detected bool) {
	now := e.deps.Now()
	source, reason := "", "claimed"
	if detected {
		source, reason = domain.ClaimSourceDetected, reasonDetected
	}
	has, err := e.deps.Store.HasClaim(drop.ID)
	if err != nil {
		e.deps.Log.Warn().Err(err).Msg("claim: history lookup failed")
	}
	if !has {
		claim := domain.Claim{
			DropID:     drop.ID,
			CampaignID: campaign.ID,
			Platform:   e.deps.Platform,
			DropName:   drop.Name,
			GameName:   campaign.GameName,
			Benefits:   drop.Benefits,
			ClaimedAt:  now,
			Source:     source,
		}
		if err := e.deps.Store.AppendClaim(claim); err != nil {
			e.deps.Log.Warn().Err(err).Msg("claim: record failed")
		}
	}
	if p, err := e.deps.Store.GetProgress(drop.ID); err == nil && p != nil {
		p.Claimable = false
		if p.ClaimedAt == nil {
			p.ClaimedAt = &now
		}
		_ = e.deps.Store.SaveProgress(*p)
	}
	_ = e.deps.Store.SetItemStatus(drop.ID, domain.ItemClaimed, reason)
	if !has {
		e.deps.Emit.Emit(EvClaimed, ClaimedPayload{DropID: drop.ID, CampaignID: campaign.ID, Name: drop.Name})
	}
	e.deps.Log.Info().Str("drop", drop.Name).Str("game", campaign.GameName).Bool("detected", detected).Msg("drop claimed")
}

// reconcileClaimedRows books every drop an inventory read reports as already
// claimed (isClaimed, i.e. ClaimedAt set) whose queue item on this platform is
// still needs_action, pending or active: the item flips to claimed with
// reasonDetected and History gets one Source "detected" entry if it has none.
// Called from the discovery pass (stored progress), the progress poll's
// inventory fetch, the sweep's inventory read; idempotent (a claimed item is
// skipped, recordClaim never duplicates History). Returns the number booked.
func (e *Engine) reconcileClaimedRows(rows []domain.Progress) int {
	claimed := map[string]bool{}
	for _, r := range rows {
		if r.ClaimedAt != nil {
			claimed[r.DropID] = true
		}
	}
	if len(claimed) == 0 {
		return 0
	}
	q, err := e.deps.Store.GetQueue()
	if err != nil {
		e.deps.Log.Warn().Err(err).Msg("claim reconcile: read queue failed")
		return 0
	}
	var byDrop map[string]sweepCandidate
	n := 0
	for _, it := range q {
		if !claimed[it.DropID] || it.Platform.OrTwitch() != e.deps.Platform.OrTwitch() {
			continue
		}
		switch it.Status {
		case domain.ItemNeedsAction, domain.ItemPending, domain.ItemActive:
		default:
			continue
		}
		if byDrop == nil {
			byDrop = e.dropsByID()
		}
		x, ok := byDrop[it.DropID]
		if !ok {
			x = sweepCandidate{drop: domain.Drop{ID: it.DropID, CampaignID: it.CampaignID, Name: it.DropID}, campaign: domain.Campaign{ID: it.CampaignID}}
		}
		e.recordClaim(x.drop, x.campaign, true)
		n++
	}
	if n > 0 {
		e.deps.Log.Info().Int("booked", n).Msg("claim reconcile: drops already claimed on the server")
	}
	return n
}

// dropsByID indexes this platform's stored campaigns by drop id.
func (e *Engine) dropsByID() map[string]sweepCandidate {
	out := map[string]sweepCandidate{}
	campaigns, err := e.deps.Store.ListCampaigns()
	if err != nil {
		e.deps.Log.Warn().Err(err).Msg("claim: list campaigns failed")
		return out
	}
	for _, c := range campaigns {
		if c.Platform.OrTwitch() != e.deps.Platform.OrTwitch() {
			continue
		}
		for _, d := range c.Drops {
			out[d.ID] = sweepCandidate{drop: d, campaign: c}
		}
	}
	return out
}

// claimIDFor returns the drop's claim id (Twitch dropInstanceID), refreshing it
// from the server when the stored progress row has none: the current drop
// session, the in-progress inventory, and last the campaign's details view,
// because Twitch removes a campaign from dropCampaignsInProgress the moment its
// drop hits the required minutes (live 2026-09-25: inventory_rows 6,
// active_in_rows false, then "no claim id known").
func (e *Engine) claimIDFor(ctx context.Context, dropID, campaignID string) string {
	p, _ := e.deps.Store.GetProgress(dropID)
	if p != nil && p.ClaimID != "" {
		return p.ClaimID
	}
	if cur, err := e.deps.Pages.FetchCurrentDrop(ctx); err == nil && cur != nil && cur.DropID == dropID && cur.ClaimID != "" {
		return e.saveClaimID(p, *cur)
	}
	rows, err := e.deps.Pages.FetchInventoryProgress(ctx)
	if err != nil && !errors.Is(err, errors.ErrUnsupported) {
		e.deps.Log.Warn().Err(err).Msg("claim: inventory read for claim id failed")
	}
	for _, r := range rows {
		if r.DropID == dropID && r.ClaimID != "" {
			return e.saveClaimID(p, r)
		}
	}
	if campaignID == "" {
		return ""
	}
	rows, err = e.deps.Pages.CampaignProgress(ctx, campaignID, "")
	if err != nil {
		if !errors.Is(err, errors.ErrUnsupported) {
			e.deps.Log.Warn().Err(err).Str("campaign", campaignID).Msg("claim: campaign details read for claim id failed")
		}
		return ""
	}
	for _, r := range rows {
		if r.DropID == dropID && r.ClaimID != "" {
			e.deps.Log.Info().Str("drop", dropID).Str("campaign", campaignID).Msg("claim: claim id learned from campaign details")
			return e.saveClaimID(p, r)
		}
	}
	return ""
}

// saveClaimID stores a freshly learned claim id on the drop's progress row.
func (e *Engine) saveClaimID(stored *domain.Progress, fresh domain.Progress) string {
	if stored == nil {
		stored = &fresh
	}
	stored.ClaimID = fresh.ClaimID
	_ = e.deps.Store.SaveProgress(*stored)
	return fresh.ClaimID
}

// claimByAPI sends the platform's claim mutation and then VERIFIES it against
// a fresh inventory read. A failure status is a failure regardless of the
// inventory. After a success status the claim counts only when the read
// succeeded (a currentUser.inventory object came back) and either shows the
// row isClaimed or no longer lists it (a fully claimed campaign leaves the
// in-progress list); a row still unclaimed is a failure. It returns the raw
// status (for logging) and nil only when the claim is confirmed. It does not
// navigate, so it is safe mid-watch.
func (e *Engine) claimByAPI(ctx context.Context, dropID, claimID string) (string, error) {
	status, raw, err := e.deps.Pages.ClaimDrop(ctx, claimID)
	if raw != "" {
		e.deps.Log.Debug().Str("drop", dropID).Str("raw", clip(raw, rawLogMax)).Msg("claim: mutation response")
	}
	if err != nil {
		return status, err
	}
	ok, how, _ := e.verifyClaimed(ctx, dropID, claimID, true)
	if !ok {
		return status, fmt.Errorf("mutation status %q not confirmed by inventory: %s", status, how)
	}
	e.deps.Log.Info().Str("drop", dropID).Str("status", status).Str("verified_by", how).Msg("claim: mutation confirmed")
	return status, nil
}

// verifyClaimed re-reads server-side inventory progress (no navigation) and
// reports whether the drop is now claimed: its row says isClaimed, or, only
// when absentMeansClaimed (the caller holds a success status from the claim
// mutation), the row is gone. A row present but unclaimed is never claimed,
// whatever its minutes. A failed read (including "no currentUser", which the
// parser reports as an error) is not claimed; err carries the read error so
// callers can tell errors.ErrUnsupported apart. how names the evidence.
func (e *Engine) verifyClaimed(ctx context.Context, dropID, claimID string, absentMeansClaimed bool) (bool, string, error) {
	rows, err := e.deps.Pages.FetchInventoryProgress(ctx)
	if err != nil {
		return false, "inventory read failed: " + err.Error(), err
	}
	for _, r := range rows {
		if r.DropID != dropID && (claimID == "" || r.ClaimID != claimID) {
			continue
		}
		if r.ClaimedAt != nil {
			return true, "is_claimed", nil
		}
		return false, "inventory still shows the drop unclaimed", nil
	}
	if absentMeansClaimed {
		return true, "row_absent", nil
	}
	return false, "drop not in the inventory (no claim evidence)", nil
}

// claimByDOM is the fallback: open the inventory page (holding the window
// visible for it), find and click the claim button (the controller waits for
// it to render and pages in more cards), write the button capture and verify. The click has no status to vouch for it, so only an isClaimed
// row confirms it; a platform with no inventory read (Kick) trusts the click.
// It reports whether the claim is confirmed.
func (e *Engine) claimByDOM(ctx, parent context.Context, dropID, claimID string) bool {
	e.setWindowMinimized(ctx, false)
	defer e.setWindowMinimized(parent, true)
	if err := e.deps.Pages.OpenInventory(ctx); err != nil {
		e.deps.Log.Warn().Err(err).Msg("claim: open inventory failed")
		return false
	}
	sel, text := e.deps.Cfg.ClaimInventorySelector(), e.deps.Cfg.Claim.InventoryButtonText
	clicked, cands, err := e.deps.Ctrl.ClaimInInventory(ctx, sel, text)
	// Captured after the attempt, which waits for the claim card to render and
	// pages in more cards, so the file shows what the matcher finally saw.
	e.captureInventoryButtons(ctx)
	if err != nil {
		e.deps.Log.Warn().Err(err).Msg("claim: inventory click failed")
		return false
	}
	if !clicked {
		if sel == "" {
			e.deps.Log.Info().Msg("claim: no inventory selector configured, wrote capture")
		} else {
			e.deps.Log.Warn().Str("selector", sel).Str("text", text).Strs("buttons", cands).Msg("claim: no inventory button matched; see debug/inventory-buttons.json")
		}
		return false
	}
	e.wait(ctx, domClaimSettle)
	ok, how, err := e.verifyClaimed(ctx, dropID, claimID, false)
	if errors.Is(err, errors.ErrUnsupported) {
		e.deps.Log.Info().Str("drop", dropID).Msg("claim: platform cannot read its inventory; trusting the click")
		return true
	}
	if !ok {
		e.deps.Log.Warn().Str("drop", dropID).Str("reason", how).Msg("claim: inventory click not confirmed")
		return false
	}
	e.deps.Log.Info().Str("drop", dropID).Str("verified_by", how).Msg("claim: inventory click confirmed")
	return true
}

// captureInventoryButtons writes every button on the current (inventory) page
// to debug/inventory-buttons.json, on every DOM fallback run and regardless of
// logging.capture_gql_bodies, so the claim selector can be fixed from real
// markup when Twitch changes it.
func (e *Engine) captureInventoryButtons(ctx context.Context) {
	b, n, err := e.deps.Pages.InventoryButtons(ctx)
	if errors.Is(err, errors.ErrUnsupported) {
		return
	}
	if err != nil {
		e.deps.Log.Warn().Err(err).Msg("claim: inventory button capture failed")
		return
	}
	path := e.dumpDebug("inventory-buttons.json", b)
	e.deps.Log.Info().Int("buttons", n).Str("file", path).Msg("claim: inventory button capture")
}

// claimSweep claims every completed, unclaimed drop of this engine's platform
// through the claim mutation (max sweepMax per pass, 2-4 s apart). Candidates
// come from ONE fresh server-side inventory read (not stored rows): minutes >=
// required, not isClaimed, claim id present. Best-effort: a failure is logged,
// never flips the item to needs_action and parks the drop for
// claim.retry_backoff (in memory, cleared on success); only a verified claim
// is booked, which also drains stuck needs_action items. The same read first
// books drops Twitch already reports claimed. The drop being watched right now
// is left to runClaiming. No navigation, no window hold. Gated by claim.sweep.
func (e *Engine) claimSweep(ctx context.Context, trigger string) {
	if !e.deps.Cfg.ClaimSweep() {
		return
	}
	var rows []domain.Progress
	err := e.bounded(ctx, func(c context.Context) (err error) { rows, err = e.deps.Pages.FetchInventoryProgress(c); return })
	if err != nil {
		if !errors.Is(err, errors.ErrUnsupported) {
			e.deps.Log.Warn().Err(err).Str("trigger", trigger).Msg("claim sweep: inventory read failed")
		}
		return
	}
	rows = append(rows, e.sweepDetailsRows(ctx, rows)...)
	e.reconcileClaimedRows(rows)
	cands := e.sweepCandidates(rows)
	if len(cands) == 0 {
		return
	}
	e.deps.Log.Info().Int("claimable", len(cands)).Str("trigger", trigger).Msg("claim sweep: start")
	for i, c := range cands {
		if i > 0 {
			e.wait(ctx, e.jittered(2*time.Second, 2*time.Second))
		}
		if ctx.Err() != nil {
			return
		}
		cctx, cancel := context.WithTimeout(ctx, claimAPITimeout)
		status, err := e.claimByAPI(cctx, c.drop.ID, c.claimID)
		cancel()
		if errors.Is(err, errors.ErrUnsupported) {
			e.deps.Log.Debug().Msg("claim sweep: platform has no claim mutation")
			return
		}
		if err != nil {
			e.sweepTried[c.drop.ID] = e.deps.Now()
			e.deps.Log.Warn().Err(err).Str("drop", c.drop.Name).Str("status", status).Msg("claim sweep: claim failed")
			continue
		}
		delete(e.sweepTried, c.drop.ID)
		e.deps.Log.Info().Str("drop", c.drop.Name).Str("path", "gql").Msg("claim: gql (sweep)")
		e.recordClaim(c.drop, c.campaign, status == gql.ClaimStatusAlreadyClaimed)
	}
}

// sweepDetailsMax caps the campaign details fetches one sweep spends on
// completed drops the inventory read no longer lists.
const sweepDetailsMax = 3

// sweepDetailsRows covers what the inventory read cannot see: a stored row
// that is complete but unclaimed (no claim on record) whose drop is missing
// from inventoryRows, because Twitch drops a campaign from
// dropCampaignsInProgress once it completes. It fetches the details view of up
// to sweepDetailsMax such campaigns and returns their rows for candidate
// gathering. A missing drop the details did not make claimable (no claim id,
// or read failed) is parked for claim.retry_backoff so it does not cost a
// details fetch on every poll. The drop being watched is left to runClaiming.
func (e *Engine) sweepDetailsRows(ctx context.Context, inventoryRows []domain.Progress) []domain.Progress {
	stored, err := e.deps.Store.ListProgress()
	if err != nil || len(stored) == 0 {
		return nil
	}
	listed := map[string]bool{}
	for _, r := range inventoryRows {
		listed[r.DropID] = true
	}
	byDrop := e.dropsByID()
	now := e.deps.Now()
	missing := map[string][]string{} // campaign id -> drop ids
	var order []string
	for _, p := range stored {
		if listed[p.DropID] || p.ClaimedAt != nil || !(p.Complete() || p.Claimable) || p.PreconditionsUnmet {
			continue
		}
		if e.cur != nil && p.DropID == e.cur.drop.ID {
			continue
		}
		if at, ok := e.sweepTried[p.DropID]; ok && now.Sub(at) < e.deps.Cfg.Claim.RetryBackoff {
			continue
		}
		x, ok := byDrop[p.DropID]
		if !ok || x.campaign.ID == "" {
			continue
		}
		if has, err := e.deps.Store.HasClaim(p.DropID); err != nil || has {
			continue
		}
		if _, seen := missing[x.campaign.ID]; !seen {
			order = append(order, x.campaign.ID)
		}
		missing[x.campaign.ID] = append(missing[x.campaign.ID], p.DropID)
	}
	var out []domain.Progress
	for i, cid := range order {
		if i >= sweepDetailsMax || ctx.Err() != nil {
			break
		}
		var rows []domain.Progress
		err := e.bounded(ctx, func(c context.Context) (err error) { rows, err = e.deps.Pages.CampaignProgress(c, cid, ""); return })
		if errors.Is(err, errors.ErrUnsupported) {
			return out
		}
		if err != nil {
			e.deps.Log.Warn().Err(err).Str("campaign", cid).Msg("claim sweep: campaign details read failed")
		}
		got := map[string]bool{}
		for _, r := range rows {
			if r.ClaimID != "" || r.ClaimedAt != nil {
				got[r.DropID] = true
			}
		}
		for _, id := range missing[cid] {
			if !got[id] {
				e.sweepTried[id] = now
			}
		}
		out = append(out, rows...)
	}
	if len(out) > 0 {
		e.deps.Log.Info().Int("campaigns", min(len(order), sweepDetailsMax)).Int("rows", len(out)).Msg("claim sweep: read campaign details for completed drops missing from the inventory")
	}
	return out
}

type sweepCandidate struct {
	drop     domain.Drop
	campaign domain.Campaign
	claimID  string
}

// sweepCandidates picks up to sweepMax claimable drops from rows, one fresh
// inventory read: server minutes >= required, not isClaimed, claim id known,
// a stored campaign of this platform, no claim on record, not parked by
// claim.retry_backoff, preconditions met, and not the drop currently watched.
func (e *Engine) sweepCandidates(rows []domain.Progress) []sweepCandidate {
	byDrop := e.dropsByID()
	now := e.deps.Now()
	seen := map[string]bool{}
	var out []sweepCandidate
	for _, r := range rows {
		if len(out) >= sweepMax {
			break
		}
		if seen[r.DropID] || (e.cur != nil && r.DropID == e.cur.drop.ID) {
			continue
		}
		if r.ClaimID == "" || r.ClaimedAt != nil || r.RequiredMinutes <= 0 || r.RealMinutes < r.RequiredMinutes {
			continue
		}
		// An earlier tier gates this one (hasPreconditionsMet false): the claim
		// would be refused, so do not spend a mutation on it.
		if r.PreconditionsUnmet {
			continue
		}
		x, ok := byDrop[r.DropID]
		if !ok {
			continue
		}
		if has, err := e.deps.Store.HasClaim(r.DropID); err != nil || has {
			continue
		}
		// A drop whose claim failed recently waits claim.retry_backoff, so a
		// stubborn one is not retried on every progress poll.
		if at, ok := e.sweepTried[r.DropID]; ok && now.Sub(at) < e.deps.Cfg.Claim.RetryBackoff {
			continue
		}
		seen[r.DropID] = true
		out = append(out, sweepCandidate{drop: x.drop, campaign: x.campaign, claimID: r.ClaimID})
	}
	return out
}

// sweepWorthy reports whether freshly polled rows show a drop other than the
// active one complete and unclaimed with a claim id and not parked by
// claim.retry_backoff, i.e. a sweep (which re-reads the inventory) has work.
func (e *Engine) sweepWorthy(ps []domain.Progress) bool {
	now := e.deps.Now()
	for _, p := range ps {
		if e.cur != nil && p.DropID == e.cur.drop.ID {
			continue
		}
		if at, ok := e.sweepTried[p.DropID]; ok && now.Sub(at) < e.deps.Cfg.Claim.RetryBackoff {
			continue
		}
		if p.ClaimID != "" && p.ClaimedAt == nil && (p.Complete() || p.Claimable) {
			return true
		}
	}
	return false
}

// waitCtx sleeps for d or until ctx is done.
func waitCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// clip truncates s to n bytes for logging.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
